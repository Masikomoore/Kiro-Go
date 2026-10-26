package proxy

import (
	"fmt"
	"kiro-go/config"
	"strings"
	"testing"
)

func resetStableSystems(t *testing.T) {
	t.Helper()
	reset := func() {
		stableSystemsMu.Lock()
		stableSystems = make(map[uint64]*stableSystemEntry)
		stableSystemsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// volatileTailTurn builds turn n of a conversation whose system prompt grows a
// line at its end every turn, like the client seen on CA2 10-26 (+51 B/turn).
func volatileTailTurn(n int) *ClaudeRequest {
	system := "You are a coding agent.\n" + strings.Repeat("Follow the project rules carefully.\n", 300)
	for i := 1; i <= n; i++ {
		system += fmt.Sprintf("<reminder turn=%d>keep going</reminder>\n", i)
	}
	msgs := []ClaudeMessage{{Role: "user", Content: "Refactor the parser package."}}
	for i := 1; i < n; i++ {
		msgs = append(msgs,
			ClaudeMessage{Role: "assistant", Content: fmt.Sprintf("Step %d done.", i)},
			ClaudeMessage{Role: "user", Content: fmt.Sprintf("Continue with step %d.", i+1)})
	}
	return &ClaudeRequest{Model: "claude-opus-5.5", System: system, Messages: msgs}
}

func TestStableSystemKeepsConversationAcrossVolatileTail(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	resetStableSystems(t)

	var payloads []*KiroPayload
	for n := 1; n <= 5; n++ {
		payloads = append(payloads, ClaudeToKiro(volatileTailTurn(n), false))
	}
	// Turn 1 has nothing to compare with; from turn 2 on the prefix is frozen.
	for n := 3; n <= 5; n++ {
		prev, cur := payloads[n-2], payloads[n-1]
		if cur.ConversationState.ConversationID != prev.ConversationState.ConversationID {
			t.Fatalf("turn %d: conversation ID changed", n)
		}
		prevHist, curHist := prev.ConversationState.History, cur.ConversationState.History
		for i := range prevHist {
			if historyEntryHash(prevHist[i]) != historyEntryHash(curHist[i]) {
				t.Fatalf("turn %d: history entry %d changed", n, i)
			}
		}
	}
	last := payloads[4]
	entry0 := last.ConversationState.History[0].UserInputMessage.Content
	if strings.Contains(entry0, "<reminder turn=") {
		t.Fatalf("volatile tail still in history entry 0")
	}
	cur := last.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(cur, "<reminder turn=5>") || !strings.HasPrefix(cur, "Continue with step 5.") {
		t.Fatalf("current message lacks the tail: %q", cur)
	}
	// Older user turns do not carry the tail.
	for _, h := range last.ConversationState.History[1:] {
		if u := h.UserInputMessage; u != nil && strings.Contains(u.Content, "<system-prompt-continued>") {
			t.Fatalf("tail leaked into history: %q", u.Content)
		}
	}
}

func TestStableSystemUnchangedPromptStaysInHistory(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	resetStableSystems(t)
	for i := 0; i < 3; i++ {
		p := ClaudeToKiro(volatileTailTurn(1), false)
		if strings.Contains(p.ConversationState.CurrentMessage.UserInputMessage.Content, "<system-prompt-continued>") {
			t.Fatalf("request %d: an unchanged system prompt was split", i)
		}
	}
}

func TestStableSystemRewrittenPromptIsNotSplit(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	resetStableSystems(t)
	a := volatileTailTurn(1)
	ClaudeToKiro(a, false)
	b := volatileTailTurn(2)
	b.System = "A completely different prompt.\n" + strings.Repeat("Other rules.\n", 400)
	p := ClaudeToKiro(b, false)
	if p.ConversationState.History[0].UserInputMessage.Content != strings.TrimSpace(b.System.(string)) {
		t.Fatalf("a rewritten system prompt must stay whole in entry 0")
	}
}

func TestStableSystemOffKeepsOldBehaviour(t *testing.T) {
	off := false
	useCompat(t, config.CompatSettings{StableSystemPrompt: &off})
	resetStableSystems(t)
	a := ClaudeToKiro(volatileTailTurn(2), false)
	b := ClaudeToKiro(volatileTailTurn(3), false)
	if a.ConversationState.ConversationID == b.ConversationState.ConversationID {
		t.Fatalf("with the switch off the conversation ID should follow the full system prompt")
	}
}
