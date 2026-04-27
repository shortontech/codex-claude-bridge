package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
)

type StreamResult struct {
	ResponseID        string
	Model             string
	StopReason        string
	Usage             anthropic.Usage
	CachedInputTokens int
}

type streamResponseRequest struct {
	Model        string         `json:"model"`
	Input        interface{}    `json:"input"`
	Instructions string         `json:"instructions,omitempty"`
	Tools        []responseTool `json:"tools,omitempty"`
	ToolChoice   string         `json:"tool_choice,omitempty"`
	Stream       bool           `json:"stream"`
	Store        bool           `json:"store"`
}

type openAIResponseUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

type openAIIncompleteDetails struct {
	Reason string `json:"reason"`
}

type openAIStreamResponse struct {
	ID                string                  `json:"id"`
	Model             string                  `json:"model"`
	Usage             openAIResponseUsage     `json:"usage"`
	IncompleteDetails openAIIncompleteDetails `json:"incomplete_details"`
	Output            []responseOutputItem    `json:"output"`
}

type openAIStreamEvent struct {
	Type        string               `json:"type"`
	Delta       string               `json:"delta"`
	Text        string               `json:"text"`
	Arguments   string               `json:"arguments"`
	OutputIndex *int                 `json:"output_index"`
	ItemID      string               `json:"item_id"`
	CallID      string               `json:"call_id"`
	Item        responseOutputItem   `json:"item"`
	Response    openAIStreamResponse `json:"response"`
}

func (c *Client) StreamFromAnthropic(
	ctx context.Context,
	req anthropic.MessagesRequest,
	model string,
	requestID string,
	onTextDelta func(string) error,
	onToolStart func(outputIndex int, callID, name string) error,
	onToolArgsDelta func(outputIndex int, partialJSON string) error,
	onToolDone func(outputIndex int) error,
) (StreamResult, error) {
	body := streamResponseRequest{
		Model:        model,
		Input:        toResponseInput(req),
		Instructions: c.resolveInstructions(req),
		Tools:        toResponseTools(req.Tools, c.toolPolicy),
		Stream:       true,
		Store:        false,
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = "required"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return StreamResult{}, err
	}
	if c.debugJSON {
		type dbgTool struct {
			Name        string `json:"name"`
			HasSchema   bool   `json:"has_schema"`
			SchemaBytes int    `json:"schema_bytes"`
		}
		tools := make([]dbgTool, 0, len(body.Tools))
		for _, t := range body.Tools {
			tools = append(tools, dbgTool{
				Name:        t.Name,
				HasSchema:   len(strings.TrimSpace(string(t.Parameters))) > 0,
				SchemaBytes: len(t.Parameters),
			})
		}
		b, _ := json.Marshal(map[string]interface{}{
			"tool_count": len(body.Tools),
			"tools":      tools,
		})
		c.debugLogJSON("upstream.stream.tools", b)
	}
	c.debugMatrixJSON(requestID, "outbound.request", "sent", true, payload)
	c.debugLogJSON("upstream.stream.request", payload)

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+c.responsesPath, bytes.NewReader(payload))
	if err != nil {
		return StreamResult{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")

	hresp, err := c.http.Do(hreq)
	if err != nil {
		return StreamResult{}, err
	}
	defer hresp.Body.Close()

	if hresp.StatusCode >= 300 {
		errPayload, readErr := io.ReadAll(hresp.Body)
		if readErr != nil {
			return StreamResult{}, readErr
		}
		c.debugMatrixJSON(requestID, "inbound.response", "error", true, errPayload)
		c.debugLogJSON(fmt.Sprintf("upstream.stream.response status=%d", hresp.StatusCode), errPayload)
		var e apiError
		_ = json.Unmarshal(errPayload, &e)
		if e.Error.Message == "" {
			e.Error.Message = string(errPayload)
		}
		return StreamResult{}, fmt.Errorf("openai error (%d): %s", hresp.StatusCode, e.Error.Message)
	}

	result := StreamResult{
		Model:      model,
		StopReason: "end_turn",
	}
	sawArgsDelta := map[int]bool{}
	sawTextDelta := map[int]bool{}
	completedTool := map[int]bool{}
	itemOutputIndex := map[string]int{}
	startedTool := map[int]bool{}
	nextSyntheticOutputIndex := -1
	sawToolCall := false
	sawDoneCall := false
	var fullText strings.Builder

	resolveOutputIndex := func(event openAIStreamEvent) int {
		if event.OutputIndex != nil {
			return *event.OutputIndex
		}
		if event.ItemID != "" {
			if idx, ok := itemOutputIndex[event.ItemID]; ok {
				return idx
			}
			idx := nextSyntheticOutputIndex
			nextSyntheticOutputIndex--
			itemOutputIndex[event.ItemID] = idx
			return idx
		}
		idx := nextSyntheticOutputIndex
		nextSyntheticOutputIndex--
		return idx
	}

	recordItemIndex := func(item responseOutputItem, outputIndex int) {
		if item.ID != "" {
			itemOutputIndex[item.ID] = outputIndex
		}
	}

	emitToolStart := func(outputIndex int, item responseOutputItem) error {
		if item.Type != "function_call" {
			return nil
		}
		if startedTool[outputIndex] {
			return nil
		}
		startedTool[outputIndex] = true
		if item.Name == doneToolName {
			sawDoneCall = true
		} else {
			sawToolCall = true
		}
		if onToolStart == nil {
			return nil
		}
		return onToolStart(outputIndex, firstNonEmpty(item.CallID, item.ID), item.Name)
	}

	err = consumeSSE(hresp.Body, func(data string) error {
		if data == "[DONE]" {
			c.debugMatrixJSON(requestID, "inbound.response", "chunk", true, []byte(`{"done":true}`))
			return nil
		}
		c.debugMatrixJSON(requestID, "inbound.response", "chunk", true, []byte(data))
		c.debugLogJSON("upstream.stream.event", []byte(data))

		var event openAIStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			anom, _ := json.Marshal(map[string]any{
				"reason": "invalid_json_event",
				"raw":    data,
			})
			c.debugMatrixJSON(requestID, "inbound.response", "drop", true, anom)
			c.debugLogJSON("upstream.stream.event.unmarshal_error", []byte(`{"error":"invalid_json_event"}`))
			return nil
		}

		if event.Response.ID != "" {
			result.ResponseID = event.Response.ID
		}
		if event.Response.Model != "" {
			result.Model = event.Response.Model
		}
		if event.Response.Usage.InputTokens > 0 || event.Response.Usage.OutputTokens > 0 || event.Response.Usage.InputTokensDetails.CachedTokens > 0 {
			result.Usage.InputTokens = event.Response.Usage.InputTokens
			result.Usage.OutputTokens = event.Response.Usage.OutputTokens
			result.CachedInputTokens = event.Response.Usage.InputTokensDetails.CachedTokens
		}

		switch event.Type {
		case "response.output_text.delta", "response.output_text.added", "response.refusal.delta":
			delta := extractDeltaText(data, event.Delta)
			if delta == "" {
				drop, _ := json.Marshal(map[string]any{"reason": "empty_text_delta", "event_type": event.Type})
				c.debugMatrixJSON(requestID, "inbound.response", "drop", true, drop)
				return nil
			}
			outputIndex := resolveOutputIndex(event)
			sawTextDelta[outputIndex] = true
			if fullText.Len() < 4096 {
				fullText.WriteString(delta)
			}
			if onTextDelta == nil {
				return nil
			}
			return onTextDelta(delta)
		case "response.output_text.done":
			outputIndex := resolveOutputIndex(event)
			if sawTextDelta[outputIndex] {
				return nil
			}
			delta := extractDeltaText(data, firstNonEmpty(event.Text, event.Delta))
			if delta == "" {
				drop, _ := json.Marshal(map[string]any{"reason": "empty_text_done", "event_type": event.Type})
				c.debugMatrixJSON(requestID, "inbound.response", "drop", true, drop)
				return nil
			}
			sawTextDelta[outputIndex] = true
			if fullText.Len() < 4096 {
				fullText.WriteString(delta)
			}
			if onTextDelta == nil {
				return nil
			}
			return onTextDelta(delta)
		case "response.output_item.added", "response.output_item.done":
			outputIndex := resolveOutputIndex(event)
			recordItemIndex(event.Item, outputIndex)
			if err := emitToolStart(outputIndex, event.Item); err != nil {
				return err
			}
			if event.Type == "response.output_item.done" && event.Item.Type == "function_call" && !completedTool[outputIndex] {
				if event.Item.Arguments != "" && !sawArgsDelta[outputIndex] {
					if onToolArgsDelta != nil {
						if err := onToolArgsDelta(outputIndex, event.Item.Arguments); err != nil {
							return err
						}
					}
				}
				if onToolDone != nil {
					if err := onToolDone(outputIndex); err != nil {
						return err
					}
				}
				completedTool[outputIndex] = true
			}
			return nil
		case "response.function_call_arguments.delta":
			outputIndex := resolveOutputIndex(event)
			if completedTool[outputIndex] {
				return nil
			}
			sawArgsDelta[outputIndex] = true
			if onToolArgsDelta == nil {
				return nil
			}
			return onToolArgsDelta(outputIndex, event.Delta)
		case "response.function_call_arguments.done":
			outputIndex := resolveOutputIndex(event)
			if completedTool[outputIndex] {
				return nil
			}
			if onToolDone == nil {
				completedTool[outputIndex] = true
				return nil
			}
			if event.Arguments != "" && !sawArgsDelta[outputIndex] {
				if onToolArgsDelta != nil {
					if err := onToolArgsDelta(outputIndex, event.Arguments); err != nil {
						return err
					}
				}
			}
			if err := onToolDone(outputIndex); err != nil {
				return err
			}
			completedTool[outputIndex] = true
			return nil
		case "response.completed":
			for _, item := range event.Response.Output {
				if item.Type != "function_call" {
					continue
				}
				outputIndex := nextSyntheticOutputIndex
				nextSyntheticOutputIndex--
				recordItemIndex(item, outputIndex)
				if err := emitToolStart(outputIndex, item); err != nil {
					return err
				}
				if completedTool[outputIndex] {
					continue
				}
				if item.Arguments != "" && onToolArgsDelta != nil {
					if err := onToolArgsDelta(outputIndex, item.Arguments); err != nil {
						return err
					}
				}
				if onToolDone != nil {
					if err := onToolDone(outputIndex); err != nil {
						return err
					}
				}
				completedTool[outputIndex] = true
			}
			if event.Response.IncompleteDetails.Reason == "max_output_tokens" {
				result.StopReason = "max_tokens"
			} else if sawToolCall {
				result.StopReason = "tool_use"
			}
		default:
			drop, _ := json.Marshal(map[string]any{"reason": "unknown_event_type", "event_type": event.Type})
			c.debugMatrixJSON(requestID, "inbound.response", "drop", true, drop)
		}

		return nil
	})
	if err != nil {
		c.debugMatrixJSON(requestID, "inbound.response", "error", true, []byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
		return StreamResult{}, err
	}
	if len(body.Tools) > 0 && !sawToolCall && !sawDoneCall {
		return StreamResult{}, fmt.Errorf("tool protocol violation: expected function call or Done")
	}
	completedPayload, _ := json.Marshal(map[string]any{
		"response_id":         result.ResponseID,
		"model":               result.Model,
		"stop_reason":         result.StopReason,
		"input_tokens":        result.Usage.InputTokens,
		"output_tokens":       result.Usage.OutputTokens,
		"cached_input_tokens": result.CachedInputTokens,
	})
	c.debugMatrixJSON(requestID, "inbound.response", "completed", true, completedPayload)
	if result.Usage.InputTokens > 0 {
		if result.CachedInputTokens <= 0 {
			log.Printf("[cache] non-caching observed model=%s input_tokens=%d cached_tokens=%d", result.Model, result.Usage.InputTokens, result.CachedInputTokens)
		} else {
			log.Printf("[cache] cache-hit model=%s input_tokens=%d cached_tokens=%d", result.Model, result.Usage.InputTokens, result.CachedInputTokens)
		}
	}

	return result, nil
}

func extractDeltaText(raw string, fallback string) string {
	if fallback != "" {
		return fallback
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}

	// Some streaming variants send delta as an object.
	if v, ok := m["delta"]; ok {
		switch d := v.(type) {
		case string:
			return d
		case map[string]interface{}:
			if t, ok := d["text"].(string); ok {
				return t
			}
			if t, ok := d["content"].(string); ok {
				return t
			}
		}
	}

	// Some variants place text directly under output_text.
	if v, ok := m["output_text"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}

	return ""
}

func consumeSSE(r io.Reader, onData func(string) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	var dataLines []string

	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if onData == nil {
			return nil
		}
		return onData(data)
	}

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
