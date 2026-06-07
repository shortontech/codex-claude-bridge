package openai

import (
	"strings"
	"testing"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
	"github.com/shortontech/codex-claude-bridge/internal/toolpolicy"
)

func TestResolveInstructionsStripsAnthropicBillingHeader(t *testing.T) {
	c := New("http://example.test", "/responses", "token", false, "", false, 0, "", "default prompt", nil, toolpolicy.Policy{})
	req := anthropic.MessagesRequest{
		System: anthropic.SystemContent{Text: strings.Join([]string{
			"x-anthropic-billing-header: cc_version=2.1.168.588; cc_entrypoint=cli; cch=e7cfc;",
			"You are Claude Code, Anthropic's official CLI for Claude.",
			injectPromptMarker,
			"",
			"gitStatus: clean",
		}, "\n")},
	}

	got := c.resolveInstructions(req)

	if strings.Contains(got, "x-anthropic-billing-header") || strings.Contains(got, "cch=") {
		t.Fatalf("volatile billing header was not stripped: %q", got)
	}
	if !strings.Contains(got, "You are Claude Code") {
		t.Fatalf("stable system text was stripped unexpectedly: %q", got)
	}
	if !strings.Contains(got, "default prompt") {
		t.Fatalf("injected default prompt was not preserved: %q", got)
	}
	if !strings.Contains(got, "gitStatus: clean") {
		t.Fatalf("stable git status text was stripped unexpectedly: %q", got)
	}
}

func TestStripAnthropicVolatileSystemLinesOnlyRemovesBillingHeader(t *testing.T) {
	got := stripAnthropicVolatileSystemLines(strings.Join([]string{
		"keep before",
		"  X-Anthropic-Billing-Header: cch=abc;  ",
		"keep after",
	}, "\n"))

	if got != "keep before\nkeep after" {
		t.Fatalf("unexpected stripped system text: %q", got)
	}
}
