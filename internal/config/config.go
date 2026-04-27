package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/toolpolicy"
)

const (
	defaultInstructionsFallback = "You are a helpful assistant."
	defaultBridgePromptPath     = "prompts/bridge_system_prompt.md"
	defaultPromptCachePath      = "codex_system_prompt.txt"
	defaultPromptURL            = "https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/prompt.md"
	maxPromptBytes              = 512 * 1024
)

type Config struct {
	Port                string
	OpenAIAPIKey        string
	OpenAIBase          string
	OpenAIResponsesPath string
	DefaultInstructions string
	DefaultModel        string
	HaikuModel          string
	ProxyAPIKey         string
	CodexAuthPath       string
	DebugJSON           bool
	DebugJSONMaxLen     int
	DebugJSONLPath      string
	ToolPolicy          toolpolicy.Policy
}

func Load() (Config, error) {
	codexAuthPath := getenv("CODEX_AUTH_JSON", "~/.codex/auth.json")
	token, _ := readCodexAccessToken(codexAuthPath)

	openAIBase := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	openAIResponsesPath := strings.TrimSpace(os.Getenv("OPENAI_RESPONSES_PATH"))
	if openAIBase == "" {
		openAIBase = "https://chatgpt.com/backend-api/codex"
	}
	if openAIResponsesPath == "" {
		openAIResponsesPath = "/responses"
	}
	defaultModel := getenv("DEFAULT_CODEX_MODEL", "gpt-5.3-codex")
	defaultHaikuModel := getenv("HAIKU_MODEL", "gpt-5.3-codex-spark")
	defaultInstructions := ResolveDefaultInstructions()
	toolPolicyPath := strings.TrimSpace(getenv("TOOL_POLICY_FILE", "config/tool_policy.yaml"))
	policy, err := toolpolicy.Load(toolPolicyPath)
	if err != nil {
		log.Printf("[tool-policy] %v", err)
	}

	cfg := Config{
		Port:                getenv("PORT", "8083"),
		OpenAIAPIKey:        token,
		OpenAIBase:          openAIBase,
		OpenAIResponsesPath: openAIResponsesPath,
		DefaultInstructions: defaultInstructions,
		DefaultModel:        defaultModel,
		HaikuModel:          defaultHaikuModel,
		ProxyAPIKey:         os.Getenv("PROXY_API_KEY"),
		CodexAuthPath:       codexAuthPath,
		DebugJSON:           strings.EqualFold(getenv("DEBUG_JSON", "false"), "true"),
		DebugJSONMaxLen:     getenvInt("DEBUG_JSON_MAX_LEN", 0),
		DebugJSONLPath:      strings.TrimSpace(os.Getenv("DEBUG_JSONL_PATH")),
		ToolPolicy:          policy,
	}

	if cfg.OpenAIAPIKey == "" {
		return Config{}, fmt.Errorf("missing credentials: provide a valid access token in %s", codexAuthPath)
	}

	return cfg, nil
}

func getenv(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func getenvInt(k string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	if n < 0 {
		return 0
	}
	return n
}

type codexAuthFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
	} `json:"tokens"`
}

func readCodexAccessToken(path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(expanded)
	if err != nil {
		return "", err
	}
	var auth codexAuthFile
	if err := json.Unmarshal(b, &auth); err != nil {
		return "", err
	}
	return strings.TrimSpace(auth.Tokens.AccessToken), nil
}

func expandHome(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func ResolveDefaultInstructions() string {
	if manual := strings.TrimSpace(os.Getenv("DEFAULT_INSTRUCTIONS")); manual != "" {
		return manual
	}

	promptPath := strings.TrimSpace(getenv("BRIDGE_SYSTEM_PROMPT_FILE", defaultBridgePromptPath))
	if promptPath != "" {
		expanded, err := expandHome(promptPath)
		if err == nil {
			if b, readErr := os.ReadFile(expanded); readErr == nil {
				if trimmed := strings.TrimSpace(string(b)); trimmed != "" {
					return trimmed
				}
			}
		}
	}

	promptURL := strings.TrimSpace(getenv("CODEX_SYSTEM_PROMPT_URL", defaultPromptURL))
	if promptURL == "" {
		return defaultInstructionsFallback
	}

	cachePath := getenv("CODEX_SYSTEM_PROMPT_CACHE", defaultPromptCachePath)
	instructions, err := loadCachedPrompt(promptURL, cachePath)
	if err != nil {
		log.Printf("[prompt-cache] %v", err)
	}
	if strings.TrimSpace(instructions) == "" {
		return defaultInstructionsFallback
	}
	return instructions
}

func loadCachedPrompt(promptURL, cachePath string) (string, error) {
	if err := validatePromptURL(promptURL); err != nil {
		return "", err
	}

	expanded, err := expandHome(cachePath)
	if err != nil {
		return "", err
	}

	var cachedModTime time.Time
	if info, err := os.Stat(expanded); err == nil {
		cachedModTime = info.ModTime().UTC()
	}

	fresh, notModified, fetchErr := fetchPrompt(promptURL, cachedModTime)
	if fetchErr == nil {
		if notModified {
			cached, readErr := os.ReadFile(expanded)
			if readErr != nil {
				return "", fmt.Errorf("prompt not modified but cache unreadable: %w", readErr)
			}
			trimmed := strings.TrimSpace(string(cached))
			if trimmed == "" {
				return "", fmt.Errorf("prompt not modified but cache is empty")
			}
			return trimmed, nil
		}
		if writeErr := writePromptCache(expanded, fresh); writeErr != nil {
			return fresh, fmt.Errorf("fetched prompt but failed writing cache at %s: %w", expanded, writeErr)
		}
		return fresh, nil
	}

	if cached, readErr := os.ReadFile(expanded); readErr == nil {
		if trimmed := strings.TrimSpace(string(cached)); trimmed != "" {
			return trimmed, nil
		}
	}

	return "", fetchErr
}

func validatePromptURL(promptURL string) error {
	u, err := url.ParseRequestURI(promptURL)
	if err != nil {
		return fmt.Errorf("invalid CODEX_SYSTEM_PROMPT_URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("invalid CODEX_SYSTEM_PROMPT_URL scheme: %s", u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("invalid CODEX_SYSTEM_PROMPT_URL host")
	}
	return nil
}

func fetchPrompt(promptURL string, cacheModTime time.Time) (string, bool, error) {
	client := http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, promptURL, nil)
	if err != nil {
		return "", false, err
	}
	if !cacheModTime.IsZero() {
		req.Header.Set("If-Modified-Since", cacheModTime.Format(http.TimeFormat))
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return "", true, nil
	}

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", false, fmt.Errorf("prompt fetch failed: %s (%s)", resp.Status, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPromptBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(body) > maxPromptBytes {
		return "", false, fmt.Errorf("prompt fetch exceeded %d bytes", maxPromptBytes)
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "", false, fmt.Errorf("prompt fetch returned empty body")
	}
	return trimmed, false, nil
}

func writePromptCache(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value+"\n"), 0o644)
}
