package pool

import (
	"kiro-go/config"
	"testing"
	"time"
)

func TestSetCooldownKeepsLaterDeadline(t *testing.T) {
	p := &AccountPool{}
	later := time.Now().Add(time.Hour)
	p.SetCooldown("a", later)
	p.SetCooldown("a", time.Now().Add(time.Minute))
	if got := p.cooldowns["a"]; !got.Equal(later) {
		t.Fatalf("shorter cooldown must not replace a later one: got %v, want %v", got, later)
	}
}

func TestRecordRateLimitedBacksOffAndHonorsRetryAfter(t *testing.T) {
	p := &AccountPool{errorCounts: make(map[string]int)}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := p.RecordRateLimited("a", 0); got != w {
			t.Fatalf("429 #%d cooldown = %s, want %s", i+1, got, w)
		}
	}
	if got := p.RecordRateLimited("b", 30*time.Second); got != 30*time.Second {
		t.Fatalf("Retry-After must win, got %s", got)
	}
	if p.errorCounts["a"] != 0 {
		t.Fatalf("429 must not count toward the consecutive-error threshold")
	}

	p.RecordSuccess("a")
	if got := p.RecordRateLimited("a", 0); got != time.Minute {
		t.Fatalf("backoff must reset after success, got %s", got)
	}
}

func TestNextAvailableIn(t *testing.T) {
	p := &AccountPool{accounts: []config.Account{{ID: "a"}, {ID: "b"}}}
	if got := p.NextAvailableIn("claude-sonnet-4.5"); got != 0 {
		t.Fatalf("free accounts must report zero, got %s", got)
	}

	p.SetCooldown("a", time.Now().Add(10*time.Minute))
	if got := p.NextAvailableIn("claude-sonnet-4.5"); got != 0 {
		t.Fatalf("one free account must report zero, got %s", got)
	}

	p.SetCooldown("b", time.Now().Add(2*time.Minute))
	got := p.NextAvailableIn("claude-sonnet-4.5")
	if got <= time.Minute || got > 2*time.Minute {
		t.Fatalf("expected about two minutes until the earliest account frees up, got %s", got)
	}
}
