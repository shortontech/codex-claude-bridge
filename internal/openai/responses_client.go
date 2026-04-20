package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/debuglog"
)

type Client struct {
	baseURL       string
	responsesPath string
	apiKey        string
	debugJSON     bool
	debugMaxLen   int
	debugJSONL    string
	defaultInstr  string
	http          *http.Client
}

func New(baseURL, responsesPath, apiKey string, debugJSON bool, debugMaxLen int, debugJSONLPath, defaultInstructions string) *Client {
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
	Store        bool           `json:"store"`
}

type responseObject struct {
	ID         string               `json:"id"`
	OutputText string               `json:"output_text"`
	Output     []responseOutputItem `json:"output"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type apiError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) CreateFromAnthropic(ctx context.Context, req anthropic.MessagesRequest, model string) (anthropic.MessagesResponse, error) {
	if req.Stream {
		return anthropic.MessagesResponse{}, fmt.Errorf("stream=true not implemented in scaffold")
	}

	body := responseRequest{
		Model:        model,
		Input:        toResponseInput(req),
		Instructions: c.resolveInstructions(req),
		Tools:        toResponseTools(req.Tools),
		Store:        false,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return anthropic.MessagesResponse{}, err
	}
	c.debugLogJSON("upstream.request", b)

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.responsesPath, bytes.NewReader(b))
	if err != nil {
		return anthropic.MessagesResponse{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
	hreq.Header.Set("Content-Type", "application/json")

	hresp, err := c.http.Do(hreq)
	if err != nil {
		return anthropic.MessagesResponse{}, err
	}
	defer hresp.Body.Close()

	payload, err := io.ReadAll(hresp.Body)
	if err != nil {
		return anthropic.MessagesResponse{}, err
	}
	c.debugLogJSON(fmt.Sprintf("upstream.response status=%d", hresp.StatusCode), payload)

	if hresp.StatusCode >= 300 {
		var e apiError
		_ = json.Unmarshal(payload, &e)
		if e.Error.Message == "" {
			e.Error.Message = string(payload)
		}
		return anthropic.MessagesResponse{}, fmt.Errorf("openai error (%d): %s", hresp.StatusCode, e.Error.Message)
	}

	var o responseObject
	if err := json.Unmarshal(payload, &o); err != nil {
		return anthropic.MessagesResponse{}, err
	}

	content, stopReason := toAnthropicBlocks(o)

	return anthropic.MessagesResponse{
		ID:         o.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    content,
		StopReason: stopReason,
		Usage: anthropic.Usage{
			InputTokens:  o.Usage.InputTokens,
			OutputTokens: o.Usage.OutputTokens,
		},
	}, nil
}

func (c *Client) resolveInstructions(req anthropic.MessagesRequest) string {
	sys := sanitizeClaudeIdentity(req.System.Text)
	if sys == "" {
		sys = c.defaultInstr
	}
	return appendBridgeExecutionPolicy(normalizeInstructionText(sys), len(req.Tools) > 0)
}

func sanitizeClaudeIdentity(s string) string {
	if s == "" {
		return s
	}
	// Claude Code injects this identity string; rewrite it to avoid confusing identity claims.
	s = strings.ReplaceAll(s, "You are Claude Code, Anthropic's official CLI for Claude.", "You are ChatGPT, running as a coding assistant in a CLI environment.")
	return s
}

func normalizeInstructionText(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		trimmed := strings.TrimSpace(line)
		// This line changes every turn and hurts upstream prompt prefix stability.
		if strings.HasPrefix(trimmed, "x-anthropic-billing-header:") {
			continue
		}
		if trimmed == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
			out = append(out, "")
			continue
		}
		prevBlank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
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

func (c *Client) debugLogJSON(prefix string, payload []byte) {
	if !c.debugJSON && c.debugJSONL == "" {
		return
	}

	if c.debugJSONL != "" {
		if err := debuglog.AppendJSONL(c.debugJSONL, "openai", prefix, payload); err != nil {
			log.Printf("[debug-json] jsonl_write_error path=%s err=%v", c.debugJSONL, err)
		}
	}

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
