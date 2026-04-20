package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	defaultHaikuModel := getenv("HAIKU_MODEL", "gpt-5.1-codex-mini")

	cfg := Config{
		Port:                getenv("PORT", "8083"),
		OpenAIAPIKey:        token,
		OpenAIBase:          openAIBase,
		OpenAIResponsesPath: openAIResponsesPath,
		DefaultInstructions: getenv("DEFAULT_INSTRUCTIONS", "You are a helpful assistant."),
		DefaultModel:        defaultModel,
		HaikuModel:          defaultHaikuModel,
		ProxyAPIKey:         os.Getenv("PROXY_API_KEY"),
		CodexAuthPath:       codexAuthPath,
		DebugJSON:           strings.EqualFold(getenv("DEBUG_JSON", "false"), "true"),
		DebugJSONMaxLen:     getenvInt("DEBUG_JSON_MAX_LEN", 0),
		DebugJSONLPath:      strings.TrimSpace(os.Getenv("DEBUG_JSONL_PATH")),
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
