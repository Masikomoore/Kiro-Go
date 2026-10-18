package proxy

import (
	"encoding/json"
	"fmt"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func kiroConversation(turns int) *KiroPayload {
	msgs := []ClaudeMessage{}
	for i := 0; i < turns; i++ {
		msgs = append(msgs, ClaudeMessage{Role: "user", Content: fmt.Sprintf("question %d: %s", i, strings.Repeat("explain the module. ", 120))})
		if i < turns-1 {
			msgs = append(msgs, ClaudeMessage{Role: "assistant", Content: fmt.Sprintf("answer %d: %s", i, strings.Repeat("it handles requests. ", 120))})
		}
	}
	return ClaudeToKiro(&ClaudeRequest{Model: "claude-sonnet-4.5", System: strings.Repeat("You are a helpful assistant. ", 100), Messages: msgs}, false)
}

func TestKiroProfileHitsGrowingConversation(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	first := tracker.BuildKiroProfile(kiroConversation(3), "claude-sonnet-4.5", 0)
	if first == nil || !first.Kiro {
		t.Fatalf("expected a Kiro profile, got %+v", first)
	}
	tracker.Update("acct-1", first)
	second := tracker.BuildKiroProfile(kiroConversation(4), "claude-sonnet-4.5", 0)
	got := tracker.Compute("acct-1", second)
	if got.CacheReadInputTokens < first.Breakpoints[len(first.Breakpoints)-1].CumulativeTokens {
		t.Fatalf("the previous turn's history must be read back, got %+v", got)
	}
}

// When kiro-go changes an early history entry (a moved truncation boundary,
// say), upstream cannot reuse the prefix past it, and neither may the report.
func TestKiroProfileStopsAtChangedEntry(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	first := kiroConversation(4)
	tracker.Update("acct-1", tracker.BuildKiroProfile(first, "claude-sonnet-4.5", 0))

	second := kiroConversation(5)
	second.ConversationState.History[2].UserInputMessage.Content = "different first kept turn"
	profile := tracker.BuildKiroProfile(second, "claude-sonnet-4.5", 0)
	got := tracker.Compute("acct-1", profile)
	limit := profile.Breakpoints[2].CumulativeTokens // prelude + entries 0..1
	if got.CacheReadInputTokens > limit {
		t.Fatalf("read %d tokens past the changed entry (limit %d)", got.CacheReadInputTokens, limit)
	}
	if got.CacheCreationInputTokens == 0 {
		t.Fatalf("the changed tail must be reported as creation, got %+v", got)
	}
}

// A WorkBuddy-style client (no session, no API key, no cache_control) stays
// on one account through the conversation fallback key, and its next turn is
// reported as a cache read of the history Kiro already saw.
func TestNoSessionConversationStaysOnAccountAndHits(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		writeKiroSuccess(t, w, "ok")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("conv-sticky")...)
	h.affinity = newAccountAffinity()

	turn := func(n int) ClaudeResponse {
		msgs := []interface{}{}
		for i := 0; i < n; i++ {
			msgs = append(msgs, map[string]interface{}{"role": "user", "content": fmt.Sprintf("question %d: %s", i, strings.Repeat("explain the module. ", 120))})
			if i < n-1 {
				msgs = append(msgs, map[string]interface{}{"role": "assistant", "content": fmt.Sprintf("answer %d: %s", i, strings.Repeat("it handles requests. ", 120))})
			}
		}
		body, _ := json.Marshal(map[string]interface{}{
			"model": "claude-sonnet-4.5", "max_tokens": 100,
			"system":   strings.Repeat("You are WorkBuddy, a desktop assistant. ", 150),
			"messages": msgs,
		})
		rec := httptest.NewRecorder()
		h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
		var resp ClaudeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return resp
	}

	first := turn(2)
	second := turn(3)
	if calls := srv.calls(); len(calls) != 2 || calls[0] != calls[1] {
		t.Fatalf("expected both turns on one account, got %v", calls)
	}
	if first.Usage.CacheReadInputTokens != 0 || first.Usage.CacheCreationInputTokens == 0 {
		t.Fatalf("first turn must create, got %+v", first.Usage)
	}
	if second.Usage.CacheReadInputTokens == 0 {
		t.Fatalf("second turn must read the first turn's history, got %+v", second.Usage)
	}
}
