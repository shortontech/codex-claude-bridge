package openai

import (
	"strings"
	"testing"
)

func TestSummarizeCacheDebugRequestUsesHashesAndCounts(t *testing.T) {
	body := streamResponseRequest{
		Model:        "gpt-test",
		Instructions: "stable secret instructions",
		Tools: []responseTool{{
			Type:       "function",
			Name:       "Bash",
			Parameters: []byte(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		}},
		Input: []responseInputItem{{
			Role: "user",
			Content: []responseInputPart{{
				Type: "input_text",
				Text: "do not leak this raw prompt",
			}},
		}},
		Stream: true,
		Store:  false,
	}
	payload := marshalForCacheDebug(body)

	got := summarizeCacheDebugRequest("req_test", true, body, payload)

	if got.RequestID != "req_test" {
		t.Fatalf("unexpected request id: %q", got.RequestID)
	}
	if got.Instructions.Bytes != len(body.Instructions) {
		t.Fatalf("unexpected instruction byte count: %d", got.Instructions.Bytes)
	}
	if got.ToolCount != 1 || len(got.ToolItems) != 1 {
		t.Fatalf("expected one summarized tool, got count=%d items=%d", got.ToolCount, len(got.ToolItems))
	}
	if got.InputItemCount != 1 || len(got.InputItems) != 1 {
		t.Fatalf("expected one summarized input item, got count=%d items=%d", got.InputItemCount, len(got.InputItems))
	}
	if got.InputItems[0].TextBytes != len("do not leak this raw prompt") {
		t.Fatalf("unexpected input text byte count: %d", got.InputItems[0].TextBytes)
	}
	if len(got.CacheBoundaryHints) != 3 {
		t.Fatalf("expected instructions/tools/input boundary hints, got %d", len(got.CacheBoundaryHints))
	}
	if got.CacheBoundaryHints[0].Label != "instructions" || got.CacheBoundaryHints[1].Label != "tools" || got.CacheBoundaryHints[2].Label != "input[0]" {
		t.Fatalf("unexpected boundary labels: %#v", got.CacheBoundaryHints)
	}

	rendered := string(marshalForCacheDebug(got))
	if strings.Contains(rendered, "do not leak this raw prompt") || strings.Contains(rendered, "stable secret instructions") {
		t.Fatalf("summary leaked raw prompt text: %s", rendered)
	}
}
