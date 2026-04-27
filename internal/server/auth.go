package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (s *Server) requireAnthropicVersion(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("anthropic-version")) == "" {
			rid := resolveRequestID(r)
			s.matrixLog(rid, "inbound.request", "rejected", false, map[string]any{"reason": "missing_anthropic_version", "path": r.URL.Path})
			s.writeError(w, http.StatusBadRequest, "invalid_request_error", "anthropic-version header is required")
			return
		}
		next(w, r)
	}
}

func (s *Server) requireProxyAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ProxyAPIKey == "" {
			next(w, r)
			return
		}

		key := strings.TrimSpace(r.Header.Get("x-api-key"))
		if key == "" {
			rid := resolveRequestID(r)
			s.matrixLog(rid, "inbound.request", "rejected", false, map[string]any{"reason": "missing_api_key", "path": r.URL.Path})
			s.writeError(w, http.StatusUnauthorized, "authentication_error", "x-api-key header is required")
			return
		}

		if subtle.ConstantTimeCompare([]byte(key), []byte(s.cfg.ProxyAPIKey)) != 1 {
			rid := resolveRequestID(r)
			s.matrixLog(rid, "inbound.request", "rejected", false, map[string]any{"reason": "invalid_api_key", "path": r.URL.Path})
			s.writeError(w, http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
			return
		}

		next(w, r)
	}
}

func (s *Server) requirePost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			rid := resolveRequestID(r)
			s.matrixLog(rid, "inbound.request", "rejected", false, map[string]any{"reason": "method_not_allowed", "method": r.Method, "path": r.URL.Path})
			s.writeError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
			return
		}
		next(w, r)
	}
}

func (s *Server) withAnthropicRequestGuards(next http.HandlerFunc) http.HandlerFunc {
	guarded := s.requirePost(s.requireProxyAPIKey(s.requireAnthropicVersion(next)))
	return func(w http.ResponseWriter, r *http.Request) {
		rid := resolveRequestID(r)
		w.Header().Set("x-request-id", rid)
		r.Header.Set("x-request-id", rid)
		guarded(w, r)
	}
}
