package proxy

import (
	"kiro-go/config"
	"strings"
	"testing"
	"time"
)

// workBuddyRequest mimics what Sub2API sends after converting a Chat
// Completions call: a long system prompt and history, no cache_control.
func workBuddyRequest(turns int) *ClaudeRequest {
	msgs := []ClaudeMessage{}
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			ClaudeMessage{Role: "user", Content: strings.Repeat("Please review the next module carefully. ", 60)},
			ClaudeMessage{Role: "assistant", Content: strings.Repeat("Here is the review of that module. ", 60)},
		)
	}
	msgs = append(msgs, ClaudeMessage{Role: "user", Content: "Continue."})
	return &ClaudeRequest{
		Model:    "claude-opus-5.5",
		System:   strings.Repeat("You are WorkBuddy, a coding assistant for the desktop app. ", 200),
		Messages: msgs,
	}
}

func TestImplicitCacheHitsOnNextTurnWithoutCacheControl(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)

	first := tracker.BuildClaudeProfile(workBuddyRequest(2), 0)
	if first == nil || !first.Implicit || len(first.Breakpoints) == 0 {
		t.Fatalf("expected an implicit profile, got %+v", first)
	}
	u1 := tracker.Compute("acct", first)
	if u1.CacheReadInputTokens != 0 || u1.CacheCreationInputTokens == 0 {
		t.Fatalf("first turn should only create, got %+v", u1)
	}
	tracker.Update("acct", first)

	// Next turn: same history plus the previous answer and a new question.
	second := tracker.BuildClaudeProfile(workBuddyRequest(3), 0)
	u2 := tracker.Compute("acct", second)
	if u2.CacheReadInputTokens == 0 {
		t.Fatalf("second turn should read the cached history, got %+v", u2)
	}
	total := estimateClaudeRequestInputTokens(workBuddyRequest(3))
	s := u2.split(total)
	if s.Input+s.Read+s.Creation != total || s.Input <= 0 {
		t.Fatalf("split %+v must add up to %d and keep the new input uncached", s, total)
	}
}

func TestImplicitCacheSkipsTheCurrentMessage(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	req := workBuddyRequest(1)
	profile := tracker.BuildClaudeProfile(req, 0)
	last := profile.Breakpoints[len(profile.Breakpoints)-1].CumulativeTokens
	total := estimateClaudeRequestInputTokens(req)
	if last >= total {
		t.Fatalf("the last message must stay outside the cached prefix: breakpoint %d, total %d", last, total)
	}
}

func TestImplicitCacheCoversSystemOnFirstTurn(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	// Sonnet's 1024-token floor; this system prompt is below Opus's 4096.
	single := workBuddyRequest(0)
	single.Model = "claude-sonnet-4.5"
	p1 := tracker.BuildClaudeProfile(single, 0)
	if p1 == nil || len(p1.Breakpoints) != 1 {
		t.Fatalf("a single message with a system prompt should have one system breakpoint, got %+v", p1)
	}
	tracker.Update("acct", p1)
	next := workBuddyRequest(1)
	next.Model = "claude-sonnet-4.5"
	if got := tracker.Compute("acct", tracker.BuildClaudeProfile(next, 0)); got.CacheReadInputTokens == 0 {
		t.Fatalf("the system prefix should be reused on the next turn, got %+v", got)
	}
}

func TestImplicitCacheSwitchOff(t *testing.T) {
	off := false
	useCompat(t, config.CompatSettings{ImplicitAnthropicCache: &off})
	tracker := newPromptCacheTracker(time.Hour)
	if p := tracker.BuildClaudeProfile(workBuddyRequest(2), 0); p != nil {
		t.Fatalf("with the switch off, a request without cache_control has no profile, got %+v", p)
	}
}

func TestExplicitCacheControlIgnoresImplicitBoundaries(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	req := cachedSystemRequest("claude-sonnet-4.5", ClaudeMessage{Role: "assistant", Content: "ok"}, ClaudeMessage{Role: "user", Content: "next"})
	withImplicit := tracker.BuildClaudeProfile(req, 0)

	off := false
	if err := config.UpdateCompatSettings(config.CompatSettings{ImplicitAnthropicCache: &off}); err != nil {
		t.Fatal(err)
	}
	without := tracker.BuildClaudeProfile(req, 0)
	if withImplicit == nil || withImplicit.Implicit || len(withImplicit.Breakpoints) != len(without.Breakpoints) {
		t.Fatalf("cache_control requests must not change with the switch: %+v vs %+v", withImplicit, without)
	}
	for i := range without.Breakpoints {
		if withImplicit.Breakpoints[i] != without.Breakpoints[i] {
			t.Fatalf("breakpoint %d differs: %+v vs %+v", i, withImplicit.Breakpoints[i], without.Breakpoints[i])
		}
	}
}
