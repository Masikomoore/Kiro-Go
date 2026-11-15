package proxy

import (
	"context"
	"testing"

	"kiro-go/config"
)

func ctxForKey(id string) context.Context {
	return context.WithValue(context.Background(), apiKeyContextKey{}, id)
}

// A key with bound accounts uses only those; other keys never use them; a
// bound key whose accounts are all excluded gets nil instead of the shared pool.
func TestNextAccountHonorsApiKeyBinding(t *testing.T) {
	h := newAffinityTestHandler(t, 4)
	bound, err := config.AddApiKey(config.ApiKeyEntry{Name: "team", Key: "sk-team", Enabled: true, AccountIDs: []string{"acc-0", "acc-1"}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := config.AddApiKey(config.ApiKeyEntry{Name: "other", Key: "sk-other", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 12; i++ {
		got := h.nextAccount(ctxForKey(bound.ID), "claude-sonnet-4.5", nil)
		if got == nil || (got.ID != "acc-0" && got.ID != "acc-1") {
			t.Fatalf("bound key got %v, want acc-0/acc-1", got)
		}
		for _, ctx := range []context.Context{ctxForKey(other.ID), context.Background()} {
			got = h.nextAccount(ctx, "claude-sonnet-4.5", nil)
			if got == nil || got.ID == "acc-0" || got.ID == "acc-1" {
				t.Fatalf("unbound caller got %v, want a shared account", got)
			}
		}
	}

	excluded := map[string]bool{"acc-0": true, "acc-1": true}
	if got := h.nextAccount(ctxForKey(bound.ID), "claude-sonnet-4.5", excluded); got != nil {
		t.Fatalf("bound key fell back to %s", got.ID)
	}
	if len(excluded) != 2 {
		t.Fatalf("caller's excluded map was modified: %v", excluded)
	}
}

func TestSetAccountApiKeysMovesBinding(t *testing.T) {
	mustInitConfig(t)
	a, _ := config.AddApiKey(config.ApiKeyEntry{Key: "sk-a", Enabled: true, AccountIDs: []string{"x"}})
	b, _ := config.AddApiKey(config.ApiKeyEntry{Key: "sk-b", Enabled: true})

	if err := config.SetAccountApiKeys("x", []string{b.ID}); err != nil {
		t.Fatal(err)
	}
	if got := config.ApiKeyIDsForAccount("x"); len(got) != 1 || got[0] != b.ID {
		t.Fatalf("keys for x = %v, want [%s]", got, b.ID)
	}
	if k := config.GetApiKeyEntry(a.ID); len(k.AccountIDs) != 0 {
		t.Fatalf("key a still binds %v", k.AccountIDs)
	}
	if err := config.SetAccountApiKeys("x", []string{"missing"}); err == nil {
		t.Fatal("unknown key id accepted")
	}
	if err := config.SetAccountApiKeys("x", nil); err != nil {
		t.Fatal(err)
	}
	if got := config.ApiKeyIDsForAccount("x"); len(got) != 0 {
		t.Fatalf("x still bound to %v", got)
	}
	if config.ApiKeyAccountFilter(a.ID) != nil {
		t.Fatal("filter should be nil once nothing is bound")
	}
}
