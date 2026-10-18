package proxy

import (
	"context"
	"fmt"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"testing"
	"time"
)

func newAffinityTestHandler(t *testing.T, accounts int) *Handler {
	t.Helper()
	mustInitConfig(t)
	for i := 0; i < accounts; i++ {
		if err := config.AddAccount(config.Account{ID: fmt.Sprintf("acc-%d", i), Email: fmt.Sprintf("a%d@example.com", i), AccessToken: "t", Enabled: true}); err != nil {
			t.Fatalf("AddAccount: %v", err)
		}
	}
	p := accountpool.GetPool()
	p.Reload()
	return &Handler{pool: p, affinity: newAccountAffinity()}
}

func TestNextAccountKeepsConversationOnOneAccount(t *testing.T) {
	h := newAffinityTestHandler(t, 3)
	ctx := withAccountAffinityKey(context.Background(), "sess:one")
	first := h.nextAccount(ctx, "claude-sonnet-4.5", nil)
	if first == nil {
		t.Fatal("no account")
	}
	for i := 0; i < 6; i++ {
		if got := h.nextAccount(ctx, "claude-sonnet-4.5", nil); got == nil || got.ID != first.ID {
			t.Fatalf("turn %d moved from %s to %v", i, first.ID, got)
		}
	}

	// Another conversation still goes through rotation, so load spreads.
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		other := withAccountAffinityKey(context.Background(), fmt.Sprintf("sess:other-%d", i))
		seen[h.nextAccount(other, "claude-sonnet-4.5", nil).ID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("new conversations should rotate across accounts, saw %v", seen)
	}
}

func TestNextAccountMovesOnWhenPreferredIsExcluded(t *testing.T) {
	h := newAffinityTestHandler(t, 2)
	ctx := withAccountAffinityKey(context.Background(), "sess:failover")
	first := h.nextAccount(ctx, "claude-sonnet-4.5", nil)
	second := h.nextAccount(ctx, "claude-sonnet-4.5", map[string]bool{first.ID: true})
	if second == nil || second.ID == first.ID {
		t.Fatalf("expected failover away from %s, got %v", first.ID, second)
	}
	// The conversation now stays on the account that took over.
	if got := h.nextAccount(ctx, "claude-sonnet-4.5", nil); got == nil || got.ID != second.ID {
		t.Fatalf("expected to stay on %s after failover, got %v", second.ID, got)
	}
}

func TestNextAccountRotatesWhenAffinityOff(t *testing.T) {
	h := newAffinityTestHandler(t, 3)
	off := false
	if err := config.UpdateCompatSettings(config.CompatSettings{AccountAffinity: &off}); err != nil {
		t.Fatalf("UpdateCompatSettings: %v", err)
	}
	ctx := withAccountAffinityKey(context.Background(), "sess:rotate")
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		seen[h.nextAccount(ctx, "claude-sonnet-4.5", nil).ID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("affinity off should rotate, saw %v", seen)
	}
}

func TestAccountAffinityExpires(t *testing.T) {
	a := newAccountAffinity()
	now := time.Now()
	a.set("k", "acc", now)
	if got := a.get("k", now.Add(accountAffinityTTL/2)); got != "acc" {
		t.Fatalf("expected live entry, got %q", got)
	}
	if got := a.get("k", now.Add(accountAffinityTTL+time.Second)); got != "" {
		t.Fatalf("expected expired entry, got %q", got)
	}
}
