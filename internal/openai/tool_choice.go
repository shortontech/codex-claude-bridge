package openai

import (
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/shortontech/codex-claude-bridge/internal/anthropic"
)

const followUpStateTTL = 45 * time.Minute

type toolChoiceDecision struct {
	Force         bool
	Reason        string
	Conversation  string
	ShortContinue bool
}

type followUpState struct {
	NeedsFollowUp bool
	Suppressed    bool
	UpdatedAt     time.Time
}

type followUpStore struct {
	mu      sync.Mutex
	entries map[string]followUpState
}

func newFollowUpStore() *followUpStore {
	return &followUpStore{entries: map[string]followUpState{}}
}

func (c *Client) shouldForceToolChoice(req anthropic.MessagesRequest) toolChoiceDecision {
	decision := toolChoiceDecision{}
	if len(req.Tools) == 0 {
		decision.Reason = "no_tools"
		return decision
	}
	decision.Conversation = conversationKey(req)
	state, ok := c.followUp.get(decision.Conversation)
	if !ok {
		decision.Reason = "no_followup_state"
		return decision
	}

	if state.Suppressed {
		decision.Reason = "suppressed_after_no_tool"
		return decision
	}
	if state.NeedsFollowUp {
		decision.Force = true
		decision.Reason = "pending_tool_followup"
		return decision
	}

	decision.Reason = "no_pending_followup"
	return decision
}

func lastUserTextAndIndex(req anthropic.MessagesRequest) (string, int) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if !strings.EqualFold(strings.TrimSpace(m.Role), "user") {
			continue
		}
		for j := len(m.Content) - 1; j >= 0; j-- {
			if m.Content[j].Type == "text" {
				return m.Content[j].Text, i
			}
		}
	}
	return "", -1
}

func firstUserText(req anthropic.MessagesRequest) string {
	for i := 0; i < len(req.Messages); i++ {
		m := req.Messages[i]
		if !strings.EqualFold(strings.TrimSpace(m.Role), "user") {
			continue
		}
		for j := 0; j < len(m.Content); j++ {
			if m.Content[j].Type == "text" {
				return m.Content[j].Text
			}
		}
	}
	return ""
}

func previousAssistantText(req anthropic.MessagesRequest, before int) string {
	for i := before - 1; i >= 0; i-- {
		m := req.Messages[i]
		if !strings.EqualFold(strings.TrimSpace(m.Role), "assistant") {
			continue
		}
		for j := len(m.Content) - 1; j >= 0; j-- {
			if m.Content[j].Type == "text" {
				return m.Content[j].Text
			}
		}
	}
	return ""
}

func assistantHasPendingAction(text string) bool {
	pending := []string{
		"i can run",
		"i can apply",
		"i can update",
		"i can change",
		"i can fix",
		"i can add",
		"i can remove",
		"i can continue",
		"i can keep going",
		"i can verify",
		"i can test",
		"i'll run",
		"i'll apply",
		"i'll update",
		"i'll change",
		"i'll fix",
		"i'll add",
		"i'll remove",
		"i'll verify",
		"i'll test",
		"i will run",
		"i will apply",
		"i will update",
		"i will change",
		"i will fix",
		"i will add",
		"i will remove",
		"i will verify",
		"i will test",
		"want me to run",
		"want me to apply",
		"want me to update",
		"want me to change",
		"want me to fix",
		"want me to add",
		"want me to remove",
		"want me to verify",
		"want me to test",
		"next steps",
	}
	for _, phrase := range pending {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func conversationKey(req anthropic.MessagesRequest) string {
	first := strings.TrimSpace(firstUserText(req))
	if first == "" {
		return ""
	}
	project := strings.ToLower(strings.TrimSpace(extractPrimaryWorkingDirectory(req.System.Text)))
	h := fnv.New64a()
	_, _ = h.Write([]byte(project))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strings.ToLower(first)))
	return stringHash(h.Sum64())
}

func stringHash(v uint64) string {
	const alphabet = "0123456789abcdef"
	b := make([]byte, 16)
	for i := 15; i >= 0; i-- {
		b[i] = alphabet[v&0xF]
		v >>= 4
	}
	return string(b)
}

func (s *followUpStore) get(key string) (followUpState, bool) {
	if strings.TrimSpace(key) == "" {
		return followUpState{}, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc(now)
	state, ok := s.entries[key]
	return state, ok
}

func (s *followUpStore) update(key string, fn func(st followUpState) followUpState) {
	if strings.TrimSpace(key) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc(now)
	st := s.entries[key]
	st = fn(st)
	st.UpdatedAt = now
	s.entries[key] = st
}

func (s *followUpStore) gc(now time.Time) {
	for key, st := range s.entries {
		if st.UpdatedAt.IsZero() {
			continue
		}
		if now.Sub(st.UpdatedAt) > followUpStateTTL {
			delete(s.entries, key)
		}
	}
}

func (c *Client) observeFollowUpOutcome(req anthropic.MessagesRequest, decision toolChoiceDecision, hadToolCall bool, assistantText string) {
	if decision.Conversation == "" {
		decision.Conversation = conversationKey(req)
	}
	if decision.Conversation == "" {
		return
	}
	c.followUp.update(decision.Conversation, func(st followUpState) followUpState {
		if hadToolCall {
			st.NeedsFollowUp = true
			st.Suppressed = false
			return st
		}
		if decision.Force {
			st.NeedsFollowUp = false
			st.Suppressed = true
			return st
		}
		st.NeedsFollowUp = false
		return st
	})
}
