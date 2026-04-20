package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
)

type countTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

func (s *Server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	var req anthropic.MessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	count := approximateInputTokens(req)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(countTokensResponse{InputTokens: count})
}

func approximateInputTokens(req anthropic.MessagesRequest) int {
	var total int

	total += countTextTokens(req.System.Text)
	for _, msg := range req.Messages {
		total += 2 // role and separators
		total += countTextTokens(msg.Role)
		for _, block := range msg.Content {
			if block.Type != "" && block.Type != "text" {
				continue
			}
			total += countTextTokens(block.Text)
		}
	}

	if total < 1 {
		return 1
	}

	return total
}

func countTextTokens(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	wordCount := len(strings.Fields(s))
	runeCount := utf8.RuneCountInString(s)
	charEstimate := (runeCount + 3) / 4
	if wordCount > charEstimate {
		return wordCount
	}
	return charEstimate
}
