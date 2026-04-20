package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
)

type messageStartEvent struct {
	Type    string        `json:"type"`
	Message streamedModel `json:"message"`
}

type streamedModel struct {
	ID           string                   `json:"id"`
	Type         string                   `json:"type"`
	Role         string                   `json:"role"`
	Model        string                   `json:"model"`
	Content      []anthropic.ContentBlock `json:"content"`
	StopReason   any                      `json:"stop_reason"`
	StopSequence any                      `json:"stop_sequence"`
	Usage        anthropic.Usage          `json:"usage"`
}

func (s *Server) handleMessagesStream(w http.ResponseWriter, r *http.Request, req anthropic.MessagesRequest, upstreamModel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "api_error", "streaming unsupported by response writer")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	messageID := fmt.Sprintf("msg_%d", time.Now().UnixNano())

	if err := writeSSEEvent(w, "message_start", messageStartEvent{
		Type: "message_start",
		Message: streamedModel{
			ID:           messageID,
			Type:         "message",
			Role:         "assistant",
			Model:        req.Model,
			Content:      []anthropic.ContentBlock{},
			StopReason:   nil,
			StopSequence: nil,
			Usage: anthropic.Usage{
				InputTokens:  0,
				OutputTokens: 0,
			},
		},
	}); err != nil {
		return
	}
	flusher.Flush()

	nextIndex := 0
	pendingText := strings.Builder{}
	sawToolUse := false
	toolBlockIndexByOutput := map[int]int{}

	result, err := s.client.StreamFromAnthropic(r.Context(), req, upstreamModel, func(delta string) error {
		if delta == "" {
			return nil
		}
		pendingText.WriteString(delta)
		return nil
	}, func(outputIndex int, callID, name string) error {
		sawToolUse = true
		pendingText.Reset()
		idx := nextIndex
		toolBlockIndexByOutput[outputIndex] = idx
		nextIndex++
		if err := writeSSEEvent(w, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": idx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    callID,
				"name":  name,
				"input": map[string]any{},
			},
		}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}, func(outputIndex int, partialJSON string) error {
		if partialJSON == "" {
			return nil
		}
		idx, ok := toolBlockIndexByOutput[outputIndex]
		if !ok {
			return nil
		}
		if err := writeSSEEvent(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": partialJSON,
			},
		}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}, func(outputIndex int) error {
		idx, ok := toolBlockIndexByOutput[outputIndex]
		if !ok {
			return nil
		}
		if err := writeSSEEvent(w, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": idx,
		}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err != nil {
		_ = writeSSEEvent(w, "message_delta", map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   "error",
				"stop_sequence": nil,
			},
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		})
		_ = writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"})
		_ = writeSSEData(w, "[DONE]")
		flusher.Flush()
		return
	}

	if !sawToolUse && pendingText.Len() > 0 {
		textBlockIndex := nextIndex
		nextIndex++
		if err := writeSSEEvent(w, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": textBlockIndex,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		}); err != nil {
			return
		}
		flusher.Flush()
		if err := writeSSEEvent(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": textBlockIndex,
			"delta": map[string]any{
				"type": "text_delta",
				"text": pendingText.String(),
			},
		}); err != nil {
			return
		}
		flusher.Flush()
		if err := writeSSEEvent(w, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		}); err != nil {
			return
		}
		flusher.Flush()
	}

	if err := writeSSEEvent(w, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   result.StopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":  result.Usage.InputTokens,
			"output_tokens": result.Usage.OutputTokens,
		},
	}); err != nil {
		return
	}
	flusher.Flush()

	if err := writeSSEEvent(w, "message_stop", map[string]any{"type": "message_stop"}); err != nil {
		return
	}
	if err := writeSSEData(w, "[DONE]"); err != nil {
		return
	}
	flusher.Flush()
}

func writeSSEEvent(w http.ResponseWriter, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	return nil
}

func writeSSEData(w http.ResponseWriter, data string) error {
	_, err := fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}
