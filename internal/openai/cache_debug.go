package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/shortontech/codex-claude-bridge/internal/debuglog"
)

type cacheDebugRequestSummary struct {
	RequestID          string                  `json:"request_id"`
	Stream             bool                    `json:"stream"`
	Model              string                  `json:"model"`
	Instructions       cacheDebugPart          `json:"instructions"`
	Tools              cacheDebugPart          `json:"tools"`
	ToolCount          int                     `json:"tool_count"`
	ToolItems          []cacheDebugTool        `json:"tool_items,omitempty"`
	Input              cacheDebugPart          `json:"input"`
	InputItemCount     int                     `json:"input_item_count"`
	InputItems         []cacheDebugInputItem   `json:"input_items,omitempty"`
	SerializedRequest  cacheDebugPart          `json:"serialized_request"`
	CacheBoundaryHints []cacheDebugPrefixEntry `json:"cache_boundary_hints,omitempty"`
}

type cacheDebugResultSummary struct {
	RequestID         string `json:"request_id"`
	Stream            bool   `json:"stream"`
	Model             string `json:"model"`
	ResponseID        string `json:"response_id,omitempty"`
	StopReason        string `json:"stop_reason,omitempty"`
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
}

type cacheDebugPart struct {
	Bytes int    `json:"bytes"`
	Hash  string `json:"hash"`
}

type cacheDebugTool struct {
	Index       int    `json:"index"`
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	SchemaBytes int    `json:"schema_bytes,omitempty"`
	Hash        string `json:"hash"`
}

type cacheDebugInputItem struct {
	Index      int    `json:"index"`
	Type       string `json:"type,omitempty"`
	Role       string `json:"role,omitempty"`
	Name       string `json:"name,omitempty"`
	CallID     string `json:"call_id,omitempty"`
	Bytes      int    `json:"bytes"`
	Hash       string `json:"hash"`
	TextBytes  int    `json:"text_bytes,omitempty"`
	PartCount  int    `json:"part_count,omitempty"`
	OutputType string `json:"output_type,omitempty"`
}

type cacheDebugPrefixEntry struct {
	Label           string `json:"label"`
	CumulativeHash  string `json:"cumulative_hash"`
	CumulativeBytes int    `json:"cumulative_bytes"`
}

func (c *Client) debugCacheRequest(requestID string, stream bool, body any, payload []byte) {
	if !c.debugCache {
		return
	}

	summary := summarizeCacheDebugRequest(requestID, stream, body, payload)
	c.appendCacheDebugJSON("cache.request", summary)
}

func (c *Client) debugCacheResult(requestID string, stream bool, result StreamResult) {
	if !c.debugCache {
		return
	}

	summary := cacheDebugResultSummary{
		RequestID:         requestID,
		Stream:            stream,
		Model:             result.Model,
		ResponseID:        result.ResponseID,
		StopReason:        result.StopReason,
		InputTokens:       result.Usage.InputTokens,
		OutputTokens:      result.Usage.OutputTokens,
		CachedInputTokens: result.CachedInputTokens,
	}
	c.appendCacheDebugJSON("cache.result", summary)
}

func (c *Client) debugCacheResponseObject(requestID string, stream bool, o responseObject) {
	if !c.debugCache {
		return
	}

	summary := cacheDebugResultSummary{
		RequestID:         requestID,
		Stream:            stream,
		Model:             o.Model,
		ResponseID:        o.ID,
		StopReason:        "end_turn",
		InputTokens:       o.Usage.InputTokens,
		OutputTokens:      o.Usage.OutputTokens,
		CachedInputTokens: o.Usage.InputTokensDetails.CachedTokens,
	}
	c.appendCacheDebugJSON("cache.result", summary)
}

func (c *Client) appendCacheDebugJSON(prefix string, v any) {
	path := strings.TrimSpace(c.debugCachePath)
	if path == "" {
		path = filepath.Join(os.TempDir(), "bridge-cache-debug.jsonl")
	}
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("[cache-debug] marshal_error prefix=%s err=%v", prefix, err)
		return
	}
	if err := debuglog.AppendJSONL(path, "openai", prefix, b); err != nil {
		log.Printf("[cache-debug] jsonl_write_error path=%s err=%v", path, err)
	}
}

func summarizeCacheDebugRequest(requestID string, stream bool, body any, payload []byte) cacheDebugRequestSummary {
	var model string
	var instructions string
	var tools []responseTool
	var input any

	switch b := body.(type) {
	case streamResponseRequest:
		model = b.Model
		instructions = b.Instructions
		tools = b.Tools
		input = b.Input
	case responseRequest:
		model = b.Model
		instructions = b.Instructions
		tools = b.Tools
		input = b.Input
	}

	inputPayload := marshalForCacheDebug(input)
	toolsPayload := marshalForCacheDebug(tools)
	summary := cacheDebugRequestSummary{
		RequestID:         requestID,
		Stream:            stream,
		Model:             model,
		Instructions:      partFromBytes([]byte(instructions)),
		Tools:             partFromBytes(toolsPayload),
		ToolCount:         len(tools),
		ToolItems:         summarizeCacheDebugTools(tools),
		Input:             partFromBytes(inputPayload),
		InputItemCount:    countCacheDebugInputItems(input),
		InputItems:        summarizeCacheDebugInput(input),
		SerializedRequest: partFromBytes(payload),
	}
	summary.CacheBoundaryHints = cacheBoundaryHints(instructions, toolsPayload, input)
	return summary
}

func summarizeCacheDebugTools(tools []responseTool) []cacheDebugTool {
	out := make([]cacheDebugTool, 0, len(tools))
	for i, tool := range tools {
		payload := marshalForCacheDebug(tool)
		out = append(out, cacheDebugTool{
			Index:       i,
			Type:        tool.Type,
			Name:        tool.Name,
			SchemaBytes: len(tool.Parameters),
			Hash:        hashBytes(payload),
		})
	}
	return out
}

func countCacheDebugInputItems(input any) int {
	items, ok := input.([]responseInputItem)
	if !ok {
		return 0
	}
	return len(items)
}

func summarizeCacheDebugInput(input any) []cacheDebugInputItem {
	items, ok := input.([]responseInputItem)
	if !ok {
		if s, ok := input.(string); ok && s == "" {
			return nil
		}
		payload := marshalForCacheDebug(input)
		return []cacheDebugInputItem{{
			Index: 0,
			Bytes: len(payload),
			Hash:  hashBytes(payload),
		}}
	}

	out := make([]cacheDebugInputItem, 0, len(items))
	for i, item := range items {
		payload := marshalForCacheDebug(item)
		out = append(out, cacheDebugInputItem{
			Index:      i,
			Type:       item.Type,
			Role:       item.Role,
			Name:       item.Name,
			CallID:     item.CallID,
			Bytes:      len(payload),
			Hash:       hashBytes(payload),
			TextBytes:  cacheDebugTextBytes(item.Content),
			PartCount:  len(item.Content),
			OutputType: cacheDebugOutputType(item.Output),
		})
	}
	return out
}

func cacheBoundaryHints(instructions string, toolsPayload []byte, input any) []cacheDebugPrefixEntry {
	var chunks [][]byte
	var labels []string

	chunks = append(chunks, []byte(instructions))
	labels = append(labels, "instructions")
	chunks = append(chunks, toolsPayload)
	labels = append(labels, "tools")

	if items, ok := input.([]responseInputItem); ok {
		for i, item := range items {
			chunks = append(chunks, marshalForCacheDebug(item))
			labels = append(labels, "input["+strconvItoa(i)+"]")
		}
	} else {
		chunks = append(chunks, marshalForCacheDebug(input))
		labels = append(labels, "input")
	}

	out := make([]cacheDebugPrefixEntry, 0, len(chunks))
	var cumulative []byte
	for i, chunk := range chunks {
		cumulative = append(cumulative, chunk...)
		out = append(out, cacheDebugPrefixEntry{
			Label:           labels[i],
			CumulativeHash:  hashBytes(cumulative),
			CumulativeBytes: len(cumulative),
		})
	}
	return out
}

func cacheDebugTextBytes(parts []responseInputPart) int {
	total := 0
	for _, part := range parts {
		total += len(part.Text)
	}
	return total
}

func cacheDebugOutputType(v any) string {
	if v == nil {
		return ""
	}
	switch v.(type) {
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "other"
	}
}

func partFromBytes(b []byte) cacheDebugPart {
	return cacheDebugPart{
		Bytes: len(b),
		Hash:  hashBytes(b),
	}
}

func marshalForCacheDebug(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("<marshal-error>")
	}
	return b
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func strconvItoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
