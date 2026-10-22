package pool

import (
	"kiro-go/config"
	"testing"
	"time"
)

func TestProxyBreakerSkipsAccountsBehindDownProxy(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "behind", ProxyURL: "socks5://10.0.0.1:1080", Priority: 0},
		config.Account{ID: "direct", Priority: 1},
	)
	p.MarkProxyDown("socks5://10.0.0.1:1080", time.Now().Add(time.Minute), "no connection")

	if got := p.GetNextForModelExcluding("claude-sonnet-4.5", nil); got == nil || got.ID != "direct" {
		t.Fatalf("with its proxy down, the account behind it must be skipped, got %+v", got)
	}
	if got := p.GetFirstForModelExcluding("claude-sonnet-4.5", nil); got == nil || got.ID != "direct" {
		t.Fatalf("priority mode must skip the account behind a down proxy, got %+v", got)
	}
	if got := p.GetNextExcluding(nil); got == nil || got.ID != "direct" {
		t.Fatalf("GetNextExcluding must skip it too, got %+v", got)
	}
	if wait := p.NextAvailableIn("claude-sonnet-4.5"); wait != 0 {
		t.Fatalf("the direct account is usable, wait=%s", wait)
	}

	p.ClearProxyDown("socks5://10.0.0.1:1080")
	if got := p.GetFirstForModelExcluding("claude-sonnet-4.5", nil); got == nil || got.ID != "behind" {
		t.Fatalf("after the breaker closes the higher-priority account must come back, got %+v", got)
	}
}

func TestProxyBreakerMovesStickyConversation(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "behind", ProxyURL: "http://proxy.example:8080"},
		config.Account{ID: "direct"},
	)
	if got := p.GetPreferredForModelExcluding("claude-sonnet-4.5", nil, "behind"); got == nil || got.ID != "behind" {
		t.Fatalf("healthy proxy keeps the conversation on its account, got %+v", got)
	}
	p.MarkProxyDown("http://proxy.example:8080", time.Now().Add(time.Minute), "refused")
	if got := p.GetPreferredForModelExcluding("claude-sonnet-4.5", nil, "behind"); got == nil || got.ID != "direct" {
		t.Fatalf("conversation must move off an account whose proxy is down, got %+v", got)
	}
}

func TestProxyBreakerFallbackPrefersHealthyProxy(t *testing.T) {
	p := newTestPool(
		config.Account{ID: "down-a", ProxyURL: "http://down.example:8080"},
		config.Account{ID: "down-b", ProxyURL: "http://down.example:8080"},
		config.Account{ID: "cooling", ProxyURL: "http://up.example:8080"},
	)
	p.MarkProxyDown("http://down.example:8080", time.Now().Add(time.Minute), "refused")
	p.SetCooldown("cooling", time.Now().Add(time.Minute))

	if got := p.GetNextForModelExcluding("claude-sonnet-4.5", nil); got == nil || got.ID != "cooling" {
		t.Fatalf("a cooling account on a healthy proxy beats one behind a down proxy, got %+v", got)
	}

	// When every candidate is behind a down proxy, one of them is still
	// returned: a request that may fail beats a 503.
	only := newTestPool(config.Account{ID: "only", ProxyURL: "http://down.example:8080"})
	only.MarkProxyDown("http://down.example:8080", time.Now().Add(time.Minute), "refused")
	if got := only.GetNextForModelExcluding("claude-sonnet-4.5", nil); got == nil || got.ID != "only" {
		t.Fatalf("with no alternative the account behind the down proxy must still be returned, got %+v", got)
	}
}

func TestProxyBreakerKeepsLaterDeadlineAndSince(t *testing.T) {
	p := &AccountPool{}
	later := time.Now().Add(time.Hour)
	if !p.MarkProxyDown("http://p.example:1", later, "first") {
		t.Fatal("first mark must report a new breaker")
	}
	if p.MarkProxyDown("http://p.example:1", time.Now().Add(time.Second), "second") {
		t.Fatal("re-marking an open breaker must not report a new one")
	}
	snap := p.ProxyHealthSnapshot()
	if len(snap) != 1 || !snap[0].Until.Equal(later) || snap[0].LastError != "second" {
		t.Fatalf("snapshot = %+v, want the later deadline and the newest error", snap)
	}
}

func TestRecordSuccessLiftsProxyBreakerOfItsAccount(t *testing.T) {
	p := newTestPool(config.Account{ID: "behind", ProxyURL: "http://proxy.example:8080"})
	p.MarkProxyDown("http://proxy.example:8080", time.Now().Add(time.Minute), "refused")
	p.RecordSuccess("behind")
	if snap := p.ProxyHealthSnapshot(); len(snap) != 0 {
		t.Fatalf("a successful request through the proxy must lift its breaker, got %+v", snap)
	}
}

func TestDirectAccountsNeverHitProxyBreaker(t *testing.T) {
	p := newTestPool(config.Account{ID: "direct"})
	p.MarkProxyDown("", time.Now().Add(time.Minute), "ignored")
	if snap := p.ProxyHealthSnapshot(); len(snap) != 0 {
		t.Fatalf("an empty proxy URL must never open a breaker, got %+v", snap)
	}
	if got := p.GetNextForModelExcluding("claude-sonnet-4.5", nil); got == nil || got.ID != "direct" {
		t.Fatalf("direct account must stay routable, got %+v", got)
	}
}
