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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/debuglog"
)

type Client struct {
	baseURL         string
	responsesPath   string
	apiKey          string
	debugJSON       bool
	debugMaxLen     int
	debugJSONL      string
	defaultInstr    string
	instrResolver   func() string
	http            *http.Client
	sessionMu       sync.RWMutex
	sessionCtx      map[string]string
	projectDirByCCH map[string]string
}

const localContextHeader = "# Local bridge context"
const customBridgeBaseInstructions = `You are ChatGPT, running as a coding assistant in a CLI harness.

Be concise, accurate, and execution-oriented.
- Prefer calling tools directly for actions that require filesystem, git, or shell access.
- Report concrete results from tool output; do not claim success without evidence.
- Preserve user intent and project conventions.
- Avoid unnecessary verbosity and avoid identity confusion with other assistants.`

var cchRegex = regexp.MustCompile(`\bcch=([A-Za-z0-9_-]+)\b`)
var claudeRepoRegex = regexp.MustCompile(`(?i)https?://github\.com/anthropics/claude-code`)
var autoMemoryHeaderRegex = regexp.MustCompile(`(?im)^\s*#{1,6}\s*auto\s+memory\b`)
var primaryWorkingDirRegex = regexp.MustCompile(`(?im)^\s*(?:-\s*)?Primary working directory:\s*(.+?)\s*$`)

func New(baseURL, responsesPath, apiKey string, debugJSON bool, debugMaxLen int, debugJSONLPath, defaultInstructions string, instrResolver func() string) *Client {
	path := strings.TrimSpace(responsesPath)
	if path == "" {
		path = "/v1/responses"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		responsesPath:   path,
		apiKey:          apiKey,
		debugJSON:       debugJSON,
		debugMaxLen:     debugMaxLen,
		debugJSONL:      strings.TrimSpace(debugJSONLPath),
		defaultInstr:    defaultInstructions,
		instrResolver:   instrResolver,
		sessionCtx:      make(map[string]string),
		projectDirByCCH: make(map[string]string),
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
	c.logCacheUsage(model, o.Usage.InputTokens, o.Usage.InputTokensDetails.CachedTokens)

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
	rawSystem := req.System.Text
	parts := []string{customBridgeBaseInstructions}

	if localCtx := c.getSessionLocalContext(rawSystem); localCtx != "" {
		parts = append(parts, localCtx)
	}

	sys := strings.TrimSpace(strings.Join(parts, "\n\n"))
	sys = scrubClaudeHarnessMentions(sys)
	sys = appendBridgeExecutionPolicy(normalizeInstructionText(sys), len(req.Tools) > 0)
	return sys
}

func scrubClaudeHarnessMentions(s string) string {
	if s == "" {
		return ""
	}
	out := s
	out = claudeRepoRegex.ReplaceAllString(out, "https://github.com/openai/codex")
	out = strings.ReplaceAll(out, "anthropics/claude-code", "openai/codex")
	out = strings.ReplaceAll(out, "Anthropic's official CLI for Claude", "a terminal coding harness")
	out = strings.ReplaceAll(out, "You are Claude Code", "You are ChatGPT")
	out = strings.ReplaceAll(out, "Claude Code", "ChatGPT CLI")
	return out
}

func looksLikeClaudeHarnessSystem(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	return strings.Contains(lower, "x-anthropic-billing-header:") ||
		strings.Contains(lower, "you are claude code, anthropic's official cli for claude.") ||
		strings.Contains(lower, "generate a concise, sentence-case title")
}

func stripClaudeHarnessInstructionLines(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "x-anthropic-billing-header:") {
			continue
		}
		if lower == "you are claude code, anthropic's official cli for claude." {
			continue
		}
		if strings.Contains(lower, "generate a concise, sentence-case title") ||
			strings.Contains(lower, "return json with a single \"title\" field") ||
			strings.HasPrefix(lower, "good examples:") ||
			strings.HasPrefix(lower, "bad (too vague):") ||
			strings.HasPrefix(lower, "bad (too long):") ||
			strings.HasPrefix(lower, "bad (wrong case):") ||
			strings.HasPrefix(trimmed, "{\"title\":") {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func (c *Client) getSessionLocalContext(rawSystem string) string {
	projectDir := c.resolveProjectDir(rawSystem)
	sessionKey := extractSessionKey(rawSystem, projectDir)

	c.sessionMu.RLock()
	if cached, ok := c.sessionCtx[sessionKey]; ok {
		c.sessionMu.RUnlock()
		return cached
	}
	c.sessionMu.RUnlock()

	built := buildLocalContextFromFiles(projectDir)

	c.sessionMu.Lock()
	if cached, ok := c.sessionCtx[sessionKey]; ok {
		c.sessionMu.Unlock()
		return cached
	}
	c.sessionCtx[sessionKey] = built
	c.sessionMu.Unlock()
	if built != "" {
		log.Printf("[instructions] loaded local context for session=%s", sessionKey)
	}
	return built
}

func extractSessionKey(rawSystem, projectDir string) string {
	if m := cchRegex.FindStringSubmatch(rawSystem); len(m) == 2 {
		if strings.TrimSpace(projectDir) != "" {
			return "cch:" + m[1] + "|pwd:" + projectDir
		}
		return "cch:" + m[1]
	}
	if strings.TrimSpace(projectDir) != "" {
		return "pwd:" + projectDir
	}
	return "default"
}

func extractCCH(rawSystem string) string {
	m := cchRegex.FindStringSubmatch(rawSystem)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func (c *Client) resolveProjectDir(rawSystem string) string {
	projectDir := extractPrimaryWorkingDirectory(rawSystem)
	cch := extractCCH(rawSystem)

	if strings.TrimSpace(projectDir) != "" {
		if cch != "" {
			c.sessionMu.Lock()
			c.projectDirByCCH[cch] = projectDir
			c.sessionMu.Unlock()
		}
		return projectDir
	}

	if cch != "" {
		c.sessionMu.RLock()
		cached := strings.TrimSpace(c.projectDirByCCH[cch])
		c.sessionMu.RUnlock()
		if cached != "" {
			return cached
		}
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

func buildLocalContextFromFiles(projectDir string) string {
	home, _ := os.UserHomeDir()

	sections := make([]string, 0, 2)
	projectRoot := strings.TrimSpace(projectDir)
	if projectRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}
	if projectRoot != "" {
		projectClaudePath := filepath.Join(projectRoot, "CLAUDE.md")
		if b, readErr := os.ReadFile(projectClaudePath); readErr == nil {
			if content, mode := extractAutoMemoryTailOrFull(string(b)); content != "" {
				sections = append(sections, fmt.Sprintf("## CLAUDE.md\n%s", content))
				log.Printf("[instructions] loaded project CLAUDE.md mode=%s path=%s", mode, projectClaudePath)
			}
		}
	}

	if home != "" {
		globalClaudePath := filepath.Join(home, ".claude", "CLAUDE.md")
		if b, readErr := os.ReadFile(globalClaudePath); readErr == nil {
			if content, mode := extractAutoMemoryTailOrFull(string(b)); content != "" {
				sections = append(sections, fmt.Sprintf("## .claude/CLAUDE.md\n%s", content))
				log.Printf("[instructions] loaded global CLAUDE.md mode=%s path=%s", mode, globalClaudePath)
			}
		}

		if projectRoot != "" {
			encodedProjectRoot := strings.ReplaceAll(projectRoot, "/", "-")
			projectMemoryPath := filepath.Join(home, ".claude", "projects", encodedProjectRoot, "memory", "MEMORY.md")
			if b, readErr := os.ReadFile(projectMemoryPath); readErr == nil {
				if content, mode := extractAutoMemoryTailOrFull(string(b)); content != "" {
					sections = append(sections, fmt.Sprintf("## .claude/projects/%s/memory/MEMORY.md\n%s", encodedProjectRoot, content))
					log.Printf("[instructions] loaded project MEMORY.md mode=%s path=%s", mode, projectMemoryPath)
				}
			}
		}
	}

	if len(sections) == 0 {
		return ""
	}
	return localContextHeader + "\n" + strings.Join(sections, "\n\n")
}

func extractAutoMemoryTail(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	idx := autoMemoryHeaderRegex.FindStringIndex(trimmed)
	if idx == nil {
		return ""
	}
	return strings.TrimSpace(trimmed[idx[0]:])
}

func extractAutoMemoryTailOrFull(s string) (string, string) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "", "empty"
	}
	idx := autoMemoryHeaderRegex.FindStringIndex(trimmed)
	if idx == nil {
		return trimmed, "full"
	}
	return strings.TrimSpace(trimmed[idx[0]:]), "auto-memory-tail"
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
