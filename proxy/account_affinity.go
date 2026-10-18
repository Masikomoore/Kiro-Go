package proxy

import (
	"context"
	"kiro-go/config"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Account affinity keeps one conversation on one upstream account.
//
// Kiro's prompt cache lives per account. Round-robin moved every turn of a
// conversation to the next account, so a long session kept re-reading its
// whole history and time to first byte grew with it. kiro-RS-Tool avoids this
// by default: its "priority" mode stays on one credential until it fails.
// Here each conversation keeps its own account, so load still spreads across
// conversations.

const (
	// accountAffinityTTL is how long an idle conversation keeps its account.
	accountAffinityTTL        = 30 * time.Minute
	accountAffinityMaxEntries = 10000
)

type accountAffinityEntry struct {
	accountID string
	lastUsed  time.Time
}

type accountAffinity struct {
	mu      sync.Mutex
	entries map[string]accountAffinityEntry
}

func newAccountAffinity() *accountAffinity {
	return &accountAffinity{entries: make(map[string]accountAffinityEntry)}
}

func (a *accountAffinity) get(key string, now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	entry, ok := a.entries[key]
	if !ok || now.Sub(entry.lastUsed) > accountAffinityTTL {
		delete(a.entries, key)
		return ""
	}
	return entry.accountID
}

func (a *accountAffinity) set(key, accountID string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries[key] = accountAffinityEntry{accountID: accountID, lastUsed: now}
	if len(a.entries) <= accountAffinityMaxEntries {
		return
	}
	// Over the cap: drop expired entries, then the oldest until back under.
	var oldestKey string
	var oldest time.Time
	for k, e := range a.entries {
		if now.Sub(e.lastUsed) > accountAffinityTTL {
			delete(a.entries, k)
			continue
		}
		if oldestKey == "" || e.lastUsed.Before(oldest) {
			oldestKey, oldest = k, e.lastUsed
		}
	}
	if len(a.entries) > accountAffinityMaxEntries {
		delete(a.entries, oldestKey)
	}
}

// accountAffinityKey identifies a conversation: the Claude Code session when
// the client sends one, otherwise the API key. Empty means no affinity.
func accountAffinityKey(req *ClaudeRequest, r *http.Request, apiKeyID string) string {
	if req != nil && req.Metadata != nil {
		if sid := claudeCodeSessionID(req.Metadata.UserID); sid != "" {
			return "sess:" + sid
		}
	}
	if r != nil {
		if sid := strings.TrimSpace(r.Header.Get("X-Claude-Code-Session-Id")); sid != "" {
			return "sess:" + sid
		}
	}
	if strings.TrimSpace(apiKeyID) != "" {
		return "key:" + apiKeyID
	}
	return ""
}

// withConversationAffinityFallback keys affinity by the Kiro conversation ID
// when the request carried neither a session nor an API key (WorkBuddy via
// Sub2API on a gateway without API keys). The ID is derived from the system
// prompt and first user message, so it is stable across a conversation's turns.
func withConversationAffinityFallback(ctx context.Context, payload *KiroPayload) context.Context {
	if accountAffinityKeyFrom(ctx) != "" || payload == nil || payload.ConversationState.ConversationID == "" {
		return ctx
	}
	return withAccountAffinityKey(ctx, "conv:"+payload.ConversationState.ConversationID)
}

type accountAffinityCtxKey struct{}

func withAccountAffinityKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, accountAffinityCtxKey{}, key)
}

func accountAffinityKeyFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	key, _ := ctx.Value(accountAffinityCtxKey{}).(string)
	return key
}

// nextAccount picks the upstream account for one attempt under
// compat.schedulingMode. In sticky mode with a conversation key in ctx, the
// conversation's previous account is reused while it is ready.
func (h *Handler) nextAccount(ctx context.Context, model string, excluded map[string]bool) *config.Account {
	switch config.GetCompatConfig().SchedulingMode {
	case config.SchedulingPriority:
		return h.pool.GetFirstForModelExcluding(model, excluded)
	case config.SchedulingRoundRobin:
		return h.pool.GetNextForModelExcluding(model, excluded)
	}
	key := accountAffinityKeyFrom(ctx)
	if key == "" || h.affinity == nil {
		return h.pool.GetNextForModelExcluding(model, excluded)
	}
	now := time.Now()
	account := h.pool.GetPreferredForModelExcluding(model, excluded, h.affinity.get(key, now))
	if account != nil {
		h.affinity.set(key, account.ID, now)
	}
	return account
}
