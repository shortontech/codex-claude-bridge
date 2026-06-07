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

func (s *Server) handleMessagesStream(w http.ResponseWriter, r *http.Request, req anthropic.MessagesRequest, upstreamModel, requestID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.matrixLog(requestID, "outbound.response", "error", true, map[string]any{"error": "streaming unsupported by response writer"})
		s.logAnthropicResponseErrorFinal(requestID, req.Stream, http.StatusInternalServerError, "api_error", "streaming unsupported by response writer")
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
		s.matrixLog(requestID, "outbound.response", "error", true, map[string]any{"error": err.Error(), "phase": "message_start"})
		s.logAnthropicResponseErrorFinal(requestID, req.Stream, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	s.matrixLog(requestID, "outbound.response", "started", true, map[string]any{"message_id": messageID, "model": req.Model})
	flusher.Flush()

	nextIndex := 0
	textOpen := false
	textBlockIndex := -1
	currentText := strings.Builder{}
	toolStartCount := 0
	toolDoneCount := 0
	toolBlockIndexByOutput := map[int]int{}
	toolNameByOutput := map[int]string{}
	toolIDByOutput := map[int]string{}
	toolArgsByOutput := map[int]string{}
	finalContent := []anthropic.ContentBlock{}

	closeTextBlock := func() error {
		if !textOpen {
			return nil
		}
		if err := writeSSEEvent(w, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		}); err != nil {
			return err
		}
		if currentText.Len() > 0 {
			finalContent = append(finalContent, anthropic.ContentBlock{Type: "text", Text: currentText.String()})
		}
		textOpen = false
		textBlockIndex = -1
		currentText.Reset()
		flusher.Flush()
		return nil
	}

	ensureTextBlock := func() error {
		if textOpen {
			return nil
		}
		idx := nextIndex
		nextIndex++
		textBlockIndex = idx
		textOpen = true
		if err := writeSSEEvent(w, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": idx,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	result, err := s.client.StreamFromAnthropic(r.Context(), req, upstreamModel, requestID, func(delta string) error {
		if delta == "" {
			return nil
		}
		if err := ensureTextBlock(); err != nil {
			return err
		}
		currentText.WriteString(delta)
		if err := writeSSEEvent(w, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": textBlockIndex,
			"delta": map[string]any{
				"type": "text_delta",
				"text": delta,
			},
		}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}, func(outputIndex int, callID, name string) error {
		if err := closeTextBlock(); err != nil {
			return err
		}
		toolNameByOutput[outputIndex] = name
		toolIDByOutput[outputIndex] = callID
		toolStartCount++
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
		toolArgsByOutput[outputIndex] += partialJSON
		idx, ok := toolBlockIndexByOutput[outputIndex]
		if !ok {
			s.matrixLog(requestID, "outbound.response", "drop", true, map[string]any{"reason": "missing_tool_block_index_args", "output_index": outputIndex})
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
		toolDoneCount++
		idx, ok := toolBlockIndexByOutput[outputIndex]
		if !ok {
			s.matrixLog(requestID, "outbound.response", "drop", true, map[string]any{"reason": "missing_tool_block_index", "output_index": outputIndex})
			return nil
		}
		if err := writeSSEEvent(w, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": idx,
		}); err != nil {
			return err
		}
		finalContent = append(finalContent, anthropic.ContentBlock{
			Type:  "tool_use",
			ID:    toolIDByOutput[outputIndex],
			Name:  toolNameByOutput[outputIndex],
			Input: parseToolInputJSON(toolArgsByOutput[outputIndex]),
		})
		flusher.Flush()
		return nil
	})
	if err != nil {
		s.matrixLog(requestID, "outbound.response", "error", true, map[string]any{"error": err.Error(), "phase": "stream_from_upstream"})
		s.logAnthropicResponseErrorFinal(requestID, req.Stream, http.StatusBadGateway, "api_error", err.Error())
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
		flusher.Flush()
		return
	}

	if err := closeTextBlock(); err != nil {
		return
	}
	s.logAnthropicResponseFinal(requestID, req.Stream, anthropic.MessagesResponse{
		ID:         result.ResponseID,
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    finalContent,
		StopReason: result.StopReason,
		Usage:      result.Usage,
	}, map[string]any{
		"tool_starts":    toolStartCount,
		"tool_completes": toolDoneCount,
	})

	s.matrixLog(requestID, "outbound.response", "completed", true, map[string]any{
		"stop_reason":    result.StopReason,
		"input_tokens":   result.Usage.InputTokens,
		"output_tokens":  result.Usage.OutputTokens,
		"text_bytes":     totalTextBytes(finalContent),
		"tool_starts":    toolStartCount,
		"tool_completes": toolDoneCount,
	})

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
	flusher.Flush()
}

func totalTextBytes(blocks []anthropic.ContentBlock) int {
	total := 0
	for _, block := range blocks {
		if block.Type == "text" {
			total += len(block.Text)
		}
	}
	return total
}

func parseToolInputJSON(s string) json.RawMessage {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	b, _ := json.Marshal(map[string]any{"_raw": trimmed})
	return json.RawMessage(b)
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
