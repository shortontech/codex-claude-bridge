package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/toolpolicy"
)

type Client struct {
	baseURL       string
	responsesPath string
	apiKey        string
	debugJSON     bool
	debugMaxLen   int
	debugJSONL    string
	defaultInstr  string
	instrResolver func() string
	http          *http.Client
	followUp      *followUpStore
	toolPolicy    toolpolicy.Policy
}

const customBridgeBaseInstructions = `You are ChatGPT, running as a coding assistant in a CLI harness.

Be concise, accurate, and execution-oriented.
- Prefer calling tools directly for actions that require filesystem, git, or shell access.
- Report concrete results from tool output; do not claim success without evidence.
- Preserve user intent and project conventions.
- Avoid unnecessary verbosity and avoid identity confusion with other assistants.

# Tool use addendum
- When tools are available and the user asks to continue, proceed, or run work, perform the required tool call(s) in the same turn instead of replying with intent-only text.
- Do not stop on acknowledgement text such as "Got it" or "I’ll do that" when a tool call is still pending.
- When the user asks for concrete repo/runtime results and tools are available, execute the measurement or command now and report results; do not ask for permission to run.`

const injectPromptMarker = "__INJECT_PROMPT__"

const executionRetryInstructions = `# Execution retry
- The prior reply was intent-only.
- Execute the required tool call(s) in this turn and report concrete results from tool output.
- Do not ask for permission or defer execution.`

var primaryWorkingDirRegex = regexp.MustCompile(`(?im)^\s*(?:-\s*)?Primary working directory:\s*(.+?)\s*$`)

func New(baseURL, responsesPath, apiKey string, debugJSON bool, debugMaxLen int, debugJSONLPath, defaultInstructions string, instrResolver func() string, policy toolpolicy.Policy) *Client {
	path := strings.TrimSpace(responsesPath)
	if path == "" {
		path = "/v1/responses"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &Client{
		baseURL:       strings.TrimRight(baseURL, "/"),
		responsesPath: path,
		apiKey:        apiKey,
		debugJSON:     debugJSON,
		debugMaxLen:   debugMaxLen,
		debugJSONL:    strings.TrimSpace(debugJSONLPath),
		defaultInstr:  defaultInstructions,
		instrResolver: instrResolver,
		followUp:      newFollowUpStore(),
		toolPolicy:    policy,
		http: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

type responseRequest struct {
	Model        string         `json:"model"`
	Input        interface{}    `json:"input"`
	Instructions string         `json:"instructions,omitempty"`
	Tools        []responseTool `json:"tools,omitempty"`
	ToolChoice   string         `json:"tool_choice,omitempty"`
	Store        bool           `json:"store"`
}

type responseObject struct {
	ID         string               `json:"id"`
	OutputText string               `json:"output_text"`
	Output     []responseOutputItem `json:"output"`
	Usage      struct {
		InputTokens        int `json:"input_tokens"`
		OutputTokens       int `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) CreateFromAnthropic(ctx context.Context, req anthropic.MessagesRequest, model, requestID string) (anthropic.MessagesResponse, error) {
	if req.Stream {
		return anthropic.MessagesResponse{}, fmt.Errorf("stream=true not implemented in scaffold")
	}
	return c.createFromAnthropicViaStream(ctx, req, model, requestID)
}

func shouldFallbackToStreaming(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "stream must be set to true")
}

func (c *Client) createFromAnthropicViaStream(ctx context.Context, req anthropic.MessagesRequest, model, requestID string) (anthropic.MessagesResponse, error) {
	type pendingTool struct {
		callID string
		name   string
		args   strings.Builder
	}
	text := strings.Builder{}
	tools := map[int]*pendingTool{}

	result, err := c.StreamFromAnthropic(ctx, req, model, requestID,
		func(delta string) error {
			text.WriteString(delta)
			return nil
		},
		func(outputIndex int, callID, name string) error {
			tools[outputIndex] = &pendingTool{callID: callID, name: name}
			return nil
		},
		func(outputIndex int, partialJSON string) error {
			if tool, ok := tools[outputIndex]; ok {
				tool.args.WriteString(partialJSON)
			}
			return nil
		},
		func(outputIndex int) error {
			return nil
		},
	)
	if err != nil {
		return anthropic.MessagesResponse{}, err
	}

	content := make([]anthropic.ContentBlock, 0, len(tools)+1)
	doneMessage := ""
	hadDone := false
	for _, tool := range tools {
		if strings.EqualFold(tool.name, doneToolName) {
			hadDone = true
			raw := strings.TrimSpace(tool.args.String())
			if raw == "" {
				continue
			}
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(raw), &args); err == nil {
				if msg := strings.TrimSpace(args.Message); msg != "" {
					doneMessage = msg
				}
			}
		}
	}
	if hadDone && strings.TrimSpace(doneMessage) == "" {
		doneMessage = "Completed."
	}
	if len(tools) > 0 {
		indices := make([]int, 0, len(tools))
		for idx := range tools {
			indices = append(indices, idx)
		}
		sort.Ints(indices)
		for _, idx := range indices {
			tool := tools[idx]
			if strings.EqualFold(tool.name, doneToolName) {
				continue
			}
			content = append(content, anthropic.ContentBlock{
				Type:  "tool_use",
				ID:    tool.callID,
				Name:  tool.name,
				Input: parseJSONOrString(tool.args.String()),
			})
		}
	}
	if len(content) == 0 && doneMessage != "" {
		content = append(content, anthropic.ContentBlock{Type: "text", Text: doneMessage})
	}
	if len(content) == 0 {
		content = append(content, anthropic.ContentBlock{Type: "text", Text: text.String()})
	}

	stopReason := result.StopReason
	if stopReason == "" {
		if len(content) > 0 && content[0].Type == "tool_use" {
			stopReason = "tool_use"
		} else {
			stopReason = "end_turn"
		}
	}

	return anthropic.MessagesResponse{
		ID:         result.ResponseID,
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    content,
		StopReason: stopReason,
		Usage: anthropic.Usage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		},
	}, nil
}

func (c *Client) callResponsesOnce(ctx context.Context, body responseRequest, stream bool, requestID string) (responseObject, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return responseObject{}, err
	}
	c.debugMatrixJSON(requestID, "outbound.request", "sent", stream, b)
	c.debugLogJSON("upstream.request", b)

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.responsesPath, bytes.NewReader(b))
	if err != nil {
		return responseObject{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
	hreq.Header.Set("Content-Type", "application/json")

	hresp, err := c.http.Do(hreq)
	if err != nil {
		return responseObject{}, err
	}
	defer hresp.Body.Close()

	payload, err := io.ReadAll(hresp.Body)
	if err != nil {
		return responseObject{}, err
	}
	c.debugMatrixJSON(requestID, "inbound.response", "received", stream, payload)
	c.debugLogJSON(fmt.Sprintf("upstream.response status=%d", hresp.StatusCode), payload)

	if hresp.StatusCode >= 300 {
		var e apiError
		_ = json.Unmarshal(payload, &e)
		if e.Error.Message == "" {
			e.Error.Message = string(payload)
		}
		return responseObject{}, fmt.Errorf("openai error (%d): %s", hresp.StatusCode, e.Error.Message)
	}

	var o responseObject
	if err := json.Unmarshal(payload, &o); err != nil {
		return responseObject{}, err
	}
	return o, nil
}

func (c *Client) debugMatrixJSON(requestID, edge, event string, stream bool, payload []byte) {
	wrapped := map[string]any{
		"request_id": requestID,
		"edge":       edge,
		"event":      event,
		"stream":     stream,
	}
	if json.Valid(payload) {
		wrapped["payload"] = json.RawMessage(payload)
	} else {
		wrapped["text"] = string(payload)
	}
	b, err := json.Marshal(wrapped)
	if err != nil {
		return
	}
	c.debugLogJSON("matrix", b)
}

func (c *Client) debugToolChoiceDecision(requestID string, stream bool, decision toolChoiceDecision) {
	payload, _ := json.Marshal(map[string]any{
		"force":             decision.Force,
		"reason":            decision.Reason,
		"short_continue":    decision.ShortContinue,
		"conversation_hash": decision.Conversation,
	})
	c.debugMatrixJSON(requestID, "policy", "tool_choice", stream, payload)
}

func (c *Client) logCacheUsage(model string, inputTokens, cachedTokens int) {
	if inputTokens <= 0 {
		return
	}
	if cachedTokens <= 0 {
		log.Printf("[cache] non-caching observed model=%s input_tokens=%d cached_tokens=%d", model, inputTokens, cachedTokens)
		return
	}
	log.Printf("[cache] cache-hit model=%s input_tokens=%d cached_tokens=%d", model, inputTokens, cachedTokens)
}

func (c *Client) resolveInstructions(req anthropic.MessagesRequest) string {
	base := c.expandInjectedPrompt(req.System.Text)
	if strings.TrimSpace(base) == "" {
		base = strings.TrimSpace(c.defaultInstr)
	}
	if strings.TrimSpace(base) == "" {
		projectDir := c.resolveProjectDir(req.System.Text)
		base = composeBaseInstructions(projectDir)
	}
	return appendBridgeExecutionPolicy(base, len(req.Tools) > 0)
}

func (c *Client) expandInjectedPrompt(systemText string) string {
	trimmed := strings.TrimSpace(systemText)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, injectPromptMarker) {
		return trimmed
	}
	return strings.ReplaceAll(trimmed, injectPromptMarker, strings.TrimSpace(c.defaultInstr))
}

func composeBaseInstructions(projectDir string) string {
	base := strings.TrimSpace(customBridgeBaseInstructions)
	if strings.TrimSpace(projectDir) == "" {
		return base
	}
	return base + "\n\n# Environment\n- Primary working directory: " + projectDir
}

func appendBridgeExecutionPolicy(instructions string, hasTools bool) string {
	if !hasTools {
		return instructions
	}
	const policy = `

# Bridge execution policy
- If tools are available and the user asks for an action that requires tools (read/write/edit/command/search), you must call the relevant tool in the same turn.
- Do not stop after an "I’m going to..." progress sentence when a tool call is still required.
- Do not claim completion unless tool output confirms the action succeeded.`

	if strings.Contains(instructions, "# Bridge execution policy") {
		return instructions
	}
	return strings.TrimSpace(instructions) + policy
}

func (c *Client) resolveProjectDir(rawSystem string) string {
	projectDir := extractPrimaryWorkingDirectory(rawSystem)
	if strings.TrimSpace(projectDir) != "" {
		return projectDir
	}

	if cwd, err := os.Getwd(); err == nil {
		return strings.TrimSpace(cwd)
	}

	return ""
}

func extractPrimaryWorkingDirectory(rawSystem string) string {
	m := primaryWorkingDirRegex.FindStringSubmatch(rawSystem)
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func (c *Client) debugLogJSON(prefix string, payload []byte) {
	if !c.debugJSON {
		return
	}
	maxLen := c.debugMaxLen
	out := string(payload)
	if maxLen > 0 && len(out) > maxLen {
		out = out[:maxLen] + "...(truncated)"
	}
	log.Printf("[debug-json] %s: %s", prefix, out)
}
