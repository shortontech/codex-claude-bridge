package openai

import (
	"encoding/json"
	"strings"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/toolpolicy"
)

type responseTool struct {
	Type       string          `json:"type"`
	Name       string          `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters json.RawMessage `json:"parameters"`
}

type responseInputItem struct {
	Type      string              `json:"type,omitempty"`
	Role      string              `json:"role,omitempty"`
	Content   []responseInputPart `json:"content,omitempty"`
	CallID    string              `json:"call_id,omitempty"`
	Name      string              `json:"name,omitempty"`
	Arguments string              `json:"arguments,omitempty"`
	Output    interface{}         `json:"output,omitempty"`
}

type responseInputPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responseOutputItem struct {
	Type      string               `json:"type"`
	ID        string               `json:"id,omitempty"`
	CallID    string               `json:"call_id,omitempty"`
	Name      string               `json:"name,omitempty"`
	Arguments string               `json:"arguments,omitempty"`
	Role      string               `json:"role,omitempty"`
	Content   []responseOutputPart `json:"content,omitempty"`
}

type responseOutputPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func toResponseInput(req anthropic.MessagesRequest) interface{} {
	items := make([]responseInputItem, 0, len(req.Messages))

	for _, m := range req.Messages {
		textParts := make([]string, 0, len(m.Content))
		for _, c := range m.Content {
			switch c.Type {
			case "text":
				if c.Text != "" {
					textParts = append(textParts, c.Text)
				}
			case "tool_result":
				items = append(items, responseInputItem{
					Type:   "function_call_output",
					CallID: c.ToolUseID,
					Output: rawJSONToToolOutput(c.Content, c.Text),
				})
			case "tool_use":
				items = append(items, responseInputItem{
					Type:      "function_call",
					CallID:    c.ID,
					Name:      c.Name,
					Arguments: rawJSONToString(c.Input, "{}"),
				})
			}
		}

		if len(textParts) == 0 {
			continue
		}

		items = append(items, responseInputItem{
			Role: normalizeRole(m.Role),
			Content: []responseInputPart{{
				Type: contentTypeForRole(m.Role),
				Text: strings.Join(textParts, "\n"),
			}},
		})
	}

	if len(items) == 0 {
		return ""
	}
	return items
}
func toResponseTools(tools []anthropic.ToolDefinition, policy toolpolicy.Policy) []responseTool {
	if len(tools) == 0 {
		return nil
	}

	out := make([]responseTool, 0, len(tools))
	for _, t := range tools {
		if !policy.IsEnabled(t.Name) {
			continue
		}
		out = append(out, responseTool{
			Type:       "function",
			Name:       t.Name,
			Description: policy.Description(t.Name, t.Description),
			Parameters: normalizeToolSchema(t.Name, t.InputSchema),
		})
	}
	return out
}

func toAnthropicBlocks(o responseObject) ([]anthropic.ContentBlock, string) {
	blocks := make([]anthropic.ContentBlock, 0, len(o.Output)+1)
	sawToolUse := false

	for _, item := range o.Output {
		switch item.Type {
		case "function_call":
			sawToolUse = true
			blocks = append(blocks, anthropic.ContentBlock{
				Type:  "tool_use",
				ID:    firstNonEmpty(item.CallID, item.ID),
				Name:  item.Name,
				Input: parseJSONOrString(item.Arguments),
			})
		case "message":
			for _, p := range item.Content {
				if p.Type == "output_text" || p.Type == "text" {
					blocks = append(blocks, anthropic.ContentBlock{
						Type: "text",
						Text: p.Text,
					})
				}
			}
		}
	}

	if len(blocks) == 0 {
		blocks = append(blocks, anthropic.ContentBlock{
			Type: "text",
			Text: o.OutputText,
		})
	}

	if sawToolUse {
		return blocks, "tool_use"
	}
	return blocks, "end_turn"
}

func normalizeRole(role string) string {
	switch strings.ToLower(role) {
	case "assistant", "system":
		return strings.ToLower(role)
	default:
		return "user"
	}
}

func contentTypeForRole(role string) string {
	if strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return "output_text"
	}
	return "input_text"
}

func rawJSONToString(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

func rawJSONToAny(raw json.RawMessage, fallbackText string) interface{} {
	if len(raw) == 0 {
		return fallbackText
	}

	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

func rawJSONToToolOutput(raw json.RawMessage, fallbackText string) interface{} {
	v := rawJSONToAny(raw, fallbackText)
	return normalizeToolOutputValue(v)
}

func normalizeToolOutputValue(v interface{}) interface{} {
	switch x := v.(type) {
	case []interface{}:
		out := make([]interface{}, 0, len(x))
		for _, item := range x {
			out = append(out, normalizeToolOutputValue(item))
		}
		return out
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = normalizeToolOutputValue(val)
		}
		// Anthropic tool_result text blocks often arrive as {type:"text",text:"..."}.
		// For function_call_output payloads, OpenAI expects input-style block types.
		if t, ok := out["type"].(string); ok {
			switch t {
			case "text", "output_text":
				out["type"] = "input_text"
			}
		}
		return out
	default:
		return v
	}
}

func normalizeObjectJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return raw
}

func normalizeToolSchema(name string, raw json.RawMessage) json.RawMessage {
	schema := normalizeObjectJSON(raw)
	lowerName := strings.ToLower(strings.TrimSpace(name))

	var obj map[string]interface{}
	if err := json.Unmarshal(schema, &obj); err != nil {
		return schema
	}
	if t, ok := obj["type"].(string); !ok || t != "object" {
		return schema
	}

	props, ok := obj["properties"].(map[string]interface{})
	if !ok {
		return schema
	}

	requiredMap := map[string][]string{
		"read":  {"file_path"},
		"write": {"file_path", "content"},
		"edit":  {"file_path", "old_string", "new_string"},
		"glob":  {"pattern"},
		"grep":  {"pattern"},
		"bash":  {"command"},
	}

	if desired, ok := requiredMap[lowerName]; ok {
		filtered := make([]string, 0, len(desired))
		for _, key := range desired {
			if _, exists := props[key]; exists {
				filtered = append(filtered, key)
			}
		}
		obj["required"] = filtered
		if b, err := json.Marshal(obj); err == nil {
			return json.RawMessage(b)
		}
	}

	return schema
}

func parseJSONOrString(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage(`{}`)
	}

	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return json.RawMessage(s)
	}

	b, _ := json.Marshal(s)
	return json.RawMessage(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
