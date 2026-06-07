package openai

import (
	"encoding/json"
	"testing"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/toolpolicy"
)

func TestToResponseToolsMapsWebSearchToHostedTool(t *testing.T) {
	tools := []anthropic.ToolDefinition{
		{
			Name:        "WebSearch",
			Description: "Search the web for sources.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"}
				},
				"required":["query"]
			}`),
		},
	}

	got := toResponseTools(tools, toolpolicy.Policy{})

	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if got[0].Type != "web_search" {
		t.Fatalf("expected hosted web_search tool, got %q", got[0].Type)
	}
	if got[0].Name != "" {
		t.Fatalf("hosted web_search tool should not include a function name, got %q", got[0].Name)
	}
	if len(got[0].Parameters) != 0 {
		t.Fatalf("hosted web_search tool should not include function parameters, got %s", got[0].Parameters)
	}

	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"type":"web_search"}` {
		t.Fatalf("unexpected hosted web search JSON: %s", b)
	}
}

func TestToResponseToolsKeepsRegularToolsAsFunctions(t *testing.T) {
	tools := []anthropic.ToolDefinition{
		{
			Name:        "Bash",
			Description: "Run shell commands.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"command":{"type":"string"},
					"description":{"type":"string"}
				},
				"required":["command","description"]
			}`),
		},
	}

	got := toResponseTools(tools, toolpolicy.Policy{})

	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if got[0].Type != "function" {
		t.Fatalf("expected function tool, got %q", got[0].Type)
	}
	if got[0].Name != "Bash" {
		t.Fatalf("expected Bash tool name, got %q", got[0].Name)
	}

	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(got[0].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "command" {
		t.Fatalf("expected Bash required fields to be normalized to command, got %#v", schema.Required)
	}
}
