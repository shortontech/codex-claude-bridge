package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/config"
	"github.com/shortontech/codex-claude-bridge/internal/debuglog"
	"github.com/shortontech/codex-claude-bridge/internal/openai"
)

type Server struct {
	cfg    config.Config
	client *openai.Client
}

func New(cfg config.Config) *Server {
	return &Server{
		cfg:    cfg,
		client: openai.New(cfg.OpenAIBase, cfg.OpenAIResponsesPath, cfg.OpenAIAPIKey, cfg.DebugJSON, cfg.DebugJSONMaxLen, cfg.DebugJSONLPath, cfg.DefaultInstructions),
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/messages", s.withAnthropicRequestGuards(s.handleMessages))
	mux.HandleFunc("/v1/messages/count_tokens", s.withAnthropicRequestGuards(s.handleCountTokens))
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "Anthropic-to-Codex bridge (scaffold)",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	var req anthropic.MessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if req.Model == "" {
		req.Model = s.cfg.DefaultModel
	}
	upstreamModel := s.resolveUpstreamModel(req.Model)
	s.debugLog("anthropic.request", req)
	if req.Stream {
		s.handleMessagesStream(w, r, req, upstreamModel)
		return
	}

	resp, err := s.client.CreateFromAnthropic(r.Context(), req, upstreamModel)
	if err != nil {
		s.writeError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	s.debugLog("anthropic.response", resp)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeError(w http.ResponseWriter, code int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	errBody := anthropic.ErrorResponse{
		Type: "error",
		Error: anthropic.ErrorPayload{
			Type:    typ,
			Message: msg,
		},
	}
	s.debugLog("anthropic.error", errBody)
	_ = json.NewEncoder(w).Encode(errBody)
}

func (s *Server) debugLog(prefix string, v any) {
	if !s.cfg.DebugJSON && s.cfg.DebugJSONLPath == "" {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("[debug-json] %s marshal_error=%v", prefix, err)
		return
	}

	if s.cfg.DebugJSONLPath != "" {
		if err := debuglog.AppendJSONL(s.cfg.DebugJSONLPath, "server", prefix, b); err != nil {
			log.Printf("[debug-json] jsonl_write_error path=%s err=%v", s.cfg.DebugJSONLPath, err)
		}
	}

	if !s.cfg.DebugJSON {
		return
	}
	maxLen := s.cfg.DebugJSONMaxLen
	out := string(b)
	if maxLen > 0 && len(out) > maxLen {
		out = out[:maxLen] + "...(truncated)"
	}
	log.Printf("[debug-json] %s: %s", prefix, out)
}

func (s *Server) resolveUpstreamModel(requestModel string) string {
	m := strings.ToLower(strings.TrimSpace(requestModel))
	if m == "" {
		return s.cfg.DefaultModel
	}
	if strings.Contains(m, "haiku") {
		return s.cfg.HaikuModel
	}
	if strings.Contains(m, "claude") {
		return s.cfg.DefaultModel
	}
	return requestModel
}
