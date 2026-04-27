package openai

import (
	"strings"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
)

func shouldRetryIntentOnlyExecution(req anthropic.MessagesRequest, hadToolCall bool) bool {
	return !hadToolCall && isActionableUserRequest(req)
}

func isActionableUserRequest(req anthropic.MessagesRequest) bool {
	if len(req.Tools) == 0 {
		return false
	}
	return hasHarnessNotification(req)
}

func hasHarnessNotification(req anthropic.MessagesRequest) bool {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if !strings.EqualFold(strings.TrimSpace(m.Role), "user") {
			continue
		}
		for _, block := range m.Content {
			if block.Type != "text" {
				continue
			}
			lower := strings.ToLower(block.Text)
			if strings.Contains(lower, "<task-notification>") || strings.Contains(lower, "<system-reminder>") {
				return true
			}
		}
		return false
	}
	return false
}
