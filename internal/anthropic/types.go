package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
)

type TextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (t *ToolDefinition) UnmarshalJSON(data []byte) error {
	var direct struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(data, &direct); err == nil {
		t.Name = strings.TrimSpace(direct.Name)
		t.Description = strings.TrimSpace(direct.Description)
		switch {
		case len(direct.InputSchema) > 0:
			t.InputSchema = direct.InputSchema
		case len(direct.Parameters) > 0:
			t.InputSchema = direct.Parameters
		}
		if t.Name != "" {
			return nil
		}
	}

	var wrapped struct {
		Type     string `json:"type"`
		Function struct {
			Name       string          `json:"name"`
			Description string         `json:"description"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Type == "function" {
		t.Name = strings.TrimSpace(wrapped.Function.Name)
		t.Description = strings.TrimSpace(wrapped.Function.Description)
		t.InputSchema = wrapped.Function.Parameters
		if t.Name != "" {
			return nil
		}
	}

	return fmt.Errorf("invalid tool definition")
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type MessageContent []ContentBlock

func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*mc = nil
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*mc = []ContentBlock{{
			Type: "text",
			Text: asString,
		}}
		return nil
	}

	var asBlocks []ContentBlock
	if err := json.Unmarshal(data, &asBlocks); err == nil {
		*mc = asBlocks
		return nil
	}

	var single ContentBlock
	if err := json.Unmarshal(data, &single); err == nil {
		*mc = []ContentBlock{single}
		return nil
	}

	return fmt.Errorf("message content must be a string, content block, or array of content blocks")
}

type Message struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
}

type SystemContent struct {
	Text string
}

func (s *SystemContent) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		s.Text = ""
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		s.Text = strings.TrimSpace(asString)
		return nil
	}

	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		s.Text = strings.TrimSpace(strings.Join(parts, "\n"))
		return nil
	}

	var single ContentBlock
	if err := json.Unmarshal(data, &single); err == nil {
		if single.Type == "text" {
			s.Text = strings.TrimSpace(single.Text)
			return nil
		}
	}

	return fmt.Errorf("system must be a string, text block, or array of text blocks")
}

type MessagesRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	Messages  []Message        `json:"messages"`
	Stream    bool             `json:"stream,omitempty"`
	System    SystemContent    `json:"system,omitempty"`
	Tools     []ToolDefinition `json:"tools,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type MessagesResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

type ErrorPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Type  string       `json:"type"`
	Error ErrorPayload `json:"error"`
}
