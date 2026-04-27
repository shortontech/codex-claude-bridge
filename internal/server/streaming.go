package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
	pendingText := strings.Builder{}
	sawToolUse := false
	toolStartCount := 0
	toolDoneCount := 0
	toolBlockIndexByOutput := map[int]int{}
	toolNameByOutput := map[int]string{}
	toolIDByOutput := map[int]string{}
	toolArgsByOutput := map[int]string{}
	doneByOutput := map[int]bool{}

	result, err := s.client.StreamFromAnthropic(r.Context(), req, upstreamModel, requestID, func(delta string) error {
		if delta == "" {
			return nil
		}
		pendingText.WriteString(delta)
		return nil
	}, func(outputIndex int, callID, name string) error {
		toolNameByOutput[outputIndex] = name
		toolIDByOutput[outputIndex] = callID
		if strings.EqualFold(name, "Done") {
			doneByOutput[outputIndex] = true
			return nil
		}
		sawToolUse = true
		toolStartCount++
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
		toolArgsByOutput[outputIndex] += partialJSON
		if doneByOutput[outputIndex] {
			return nil
		}
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
		if doneByOutput[outputIndex] {
			return nil
		}
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
		_ = writeSSEData(w, "[DONE]")
		flusher.Flush()
		return
	}

	doneMessage := extractDoneMessage(toolNameByOutput, toolArgsByOutput)
	if len(doneByOutput) > 0 && strings.TrimSpace(doneMessage) == "" {
		doneMessage = "Completed."
	}
	visibleText := strings.TrimSpace(doneMessage)
	if visibleText == "" {
		visibleText = pendingText.String()
	}
	if !sawToolUse && strings.TrimSpace(visibleText) != "" {
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
				"text": visibleText,
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

	finalContent := make([]anthropic.ContentBlock, 0, len(toolNameByOutput)+1)
	if sawToolUse {
		order := make([]int, 0, len(toolNameByOutput))
		for outputIndex := range toolNameByOutput {
			if doneByOutput[outputIndex] {
				continue
			}
			order = append(order, outputIndex)
		}
		sort.Ints(order)
		for _, outputIndex := range order {
			finalContent = append(finalContent, anthropic.ContentBlock{
				Type:  "tool_use",
				ID:    toolIDByOutput[outputIndex],
				Name:  toolNameByOutput[outputIndex],
				Input: parseToolInputJSON(toolArgsByOutput[outputIndex]),
			})
		}
	} else if strings.TrimSpace(doneMessage) != "" {
		finalContent = append(finalContent, anthropic.ContentBlock{Type: "text", Text: doneMessage})
	} else if pendingText.Len() > 0 {
		finalContent = append(finalContent, anthropic.ContentBlock{Type: "text", Text: pendingText.String()})
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
		"done_calls":     len(doneByOutput),
		"done_message":   strings.TrimSpace(doneMessage) != "",
	})

	s.matrixLog(requestID, "outbound.response", "completed", true, map[string]any{
		"stop_reason":    result.StopReason,
		"input_tokens":   result.Usage.InputTokens,
		"output_tokens":  result.Usage.OutputTokens,
		"text_bytes":     pendingText.Len(),
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
	if err := writeSSEData(w, "[DONE]"); err != nil {
		return
	}
	flusher.Flush()
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

func extractDoneMessage(toolNameByOutput map[int]string, toolArgsByOutput map[int]string) string {
	indices := make([]int, 0, len(toolNameByOutput))
	for idx, name := range toolNameByOutput {
		if strings.EqualFold(name, "Done") {
			indices = append(indices, idx)
		}
	}
	if len(indices) == 0 {
		return ""
	}
	sort.Ints(indices)
	for i := len(indices) - 1; i >= 0; i-- {
		idx := indices[i]
		raw := strings.TrimSpace(toolArgsByOutput[idx])
		if raw == "" {
			continue
		}
		var args struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(raw), &args); err == nil {
			if msg := strings.TrimSpace(args.Message); msg != "" {
				return msg
			}
		}
	}
	return ""
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
