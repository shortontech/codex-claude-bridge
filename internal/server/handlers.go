package server

import (
	"crypto/rand"
	"encoding/hex"
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
		client: openai.New(cfg.OpenAIBase, cfg.OpenAIResponsesPath, cfg.OpenAIAPIKey, cfg.DebugJSON, cfg.DebugJSONMaxLen, cfg.DebugJSONLPath, cfg.DefaultInstructions, config.ResolveDefaultInstructions, cfg.ToolPolicy),
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
	requestID := resolveRequestID(r)
	w.Header().Set("x-request-id", requestID)

	var req anthropic.MessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.matrixLog(requestID, "inbound.request", "decode_error", false, map[string]any{"error": err.Error()})
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if req.Model == "" {
		req.Model = s.cfg.DefaultModel
	}
	upstreamModel := s.resolveUpstreamModel(req.Model)
	s.logAnthropicRequestFinal(requestID, req, upstreamModel)
	s.matrixLog(requestID, "inbound.request", "received", req.Stream, req)
	s.matrixLog(requestID, "outbound.request", "prepared", req.Stream, map[string]any{
		"upstream_model":  upstreamModel,
		"requested_model": req.Model,
	})
	if req.Stream {
		s.handleMessagesStream(w, r, req, upstreamModel, requestID)
		return
	}

	resp, err := s.client.CreateFromAnthropic(r.Context(), req, upstreamModel, requestID)
	if err != nil {
		s.matrixLog(requestID, "outbound.response", "error", false, map[string]any{"error": err.Error()})
		s.logAnthropicResponseErrorFinal(requestID, req.Stream, http.StatusBadGateway, "api_error", err.Error())
		s.writeError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	s.logAnthropicResponseFinal(requestID, req.Stream, resp, nil)
	s.matrixLog(requestID, "outbound.response", "sent", false, resp)

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
		if shouldWriteJSONLPrefix(prefix) {
			if err := debuglog.AppendJSONL(s.cfg.DebugJSONLPath, "server", prefix, b); err != nil {
				log.Printf("[debug-json] jsonl_write_error path=%s err=%v", s.cfg.DebugJSONLPath, err)
			}
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

func (s *Server) matrixLog(requestID, edge, event string, stream bool, payload any) {
	record := map[string]any{
		"request_id": requestID,
		"edge":       edge,
		"event":      event,
		"stream":     stream,
		"payload":    payload,
	}
	s.debugLog("matrix", record)
}

func shouldWriteJSONLPrefix(prefix string) bool {
	switch prefix {
	case "anthropic.request.final", "anthropic.response.final":
		return true
	default:
		return false
	}
}

func (s *Server) logAnthropicRequestFinal(requestID string, req anthropic.MessagesRequest, upstreamModel string) {
	payload := map[string]any{
		"request_id": requestID,
		"stream":     req.Stream,
		"request":    req,
		"indicators": map[string]any{
			"requested_model":    req.Model,
			"upstream_model":     upstreamModel,
			"tools_len":          len(req.Tools),
			"tool_result_blocks": countToolResultBlocks(req),
		},
	}
	s.debugLog("anthropic.request.final", payload)
}

func (s *Server) logAnthropicResponseFinal(requestID string, stream bool, resp anthropic.MessagesResponse, extra map[string]any) {
	indicators := map[string]any{
		"stop_reason":       resp.StopReason,
		"tool_uses_count":   countToolUseBlocks(resp.Content),
		"text_blocks_count": countTextBlocks(resp.Content),
	}
	for k, v := range extra {
		indicators[k] = v
	}
	payload := map[string]any{
		"request_id": requestID,
		"stream":     stream,
		"response":   resp,
		"indicators": indicators,
	}
	s.debugLog("anthropic.response.final", payload)
}

func (s *Server) logAnthropicResponseErrorFinal(requestID string, stream bool, code int, typ, msg string) {
	payload := map[string]any{
		"request_id": requestID,
		"stream":     stream,
		"error": anthropic.ErrorResponse{
			Type: "error",
			Error: anthropic.ErrorPayload{
				Type:    typ,
				Message: msg,
			},
		},
		"indicators": map[string]any{
			"status_code": code,
		},
	}
	s.debugLog("anthropic.response.final", payload)
}

func countToolResultBlocks(req anthropic.MessagesRequest) int {
	count := 0
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "tool_result" {
				count++
			}
		}
	}
	return count
}

func countToolUseBlocks(blocks []anthropic.ContentBlock) int {
	count := 0
	for _, b := range blocks {
		if b.Type == "tool_use" {
			count++
		}
	}
	return count
}

func countTextBlocks(blocks []anthropic.ContentBlock) int {
	count := 0
	for _, b := range blocks {
		if b.Type == "text" {
			count++
		}
	}
	return count
}

func resolveRequestID(r *http.Request) string {
	if r != nil {
		if existing := strings.TrimSpace(r.Header.Get("x-request-id")); existing != "" {
			return existing
		}
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b)
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
