package proxy

import (
	"encoding/json"
	"hash/fnv"
	"kiro-go/logger"
	"strconv"
	"sync"
	"time"
)

// Prompt-cache diagnostics.
//
// Kiro caches prompt prefixes upstream but reports no cache tokens, and some
// conversations are billed at full price every turn although their history
// only grows. To find where the prefix breaks, each /v1/messages request is
// compared with the previous request of the same conversation at two layers:
// the client's messages (did the client or a converter such as Sub2API change
// earlier turns?) and the Kiro history kiro-go sends upstream (did the
// conversion or payload truncation change them?). Only hashes and indexes are
// logged, never content.

const (
	cacheDiagTTL        = 30 * time.Minute
	cacheDiagMaxEntries = 512
)

type cacheDiagEntry struct {
	client     []uint64
	clientNoID []uint64
	kiro       []uint64
	kiroNoID   []uint64
	tools      uint64
	seen       time.Time
}

type cacheDiagTracker struct {
	mu      sync.Mutex
	entries map[string]*cacheDiagEntry
}

func newCacheDiagTracker() *cacheDiagTracker {
	return &cacheDiagTracker{entries: make(map[string]*cacheDiagEntry)}
}

func diagHash(v interface{}) uint64 {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write(raw)
	return h.Sum64()
}

// clientMessageHashes hashes each client message as sent, and again with tool
// IDs removed, so a divergence caused only by regenerated IDs is visible.
func clientMessageHashes(messages []ClaudeMessage) (full, noID []uint64) {
	for _, msg := range messages {
		full = append(full, diagHash(msg))
		content := msg.Content
		if blocks, ok := content.([]interface{}); ok {
			stripped := make([]interface{}, len(blocks))
			for i, block := range blocks {
				stripped[i] = stripVolatileToolIDs(block)
			}
			content = stripped
		}
		noID = append(noID, diagHash(map[string]interface{}{"role": msg.Role, "content": content}))
	}
	return full, noID
}

// kiroHistoryHashes hashes each Kiro history entry, and again with tool use
// IDs blanked.
func kiroHistoryHashes(history []KiroHistoryMessage) (full, noID []uint64) {
	for _, entry := range history {
		full = append(full, diagHash(entry))
		clone := entry
		if u := entry.UserInputMessage; u != nil {
			uc := *u
			if ctx := u.UserInputMessageContext; ctx != nil {
				cc := *ctx
				cc.ToolResults = make([]KiroToolResult, len(ctx.ToolResults))
				for i, r := range ctx.ToolResults {
					r.ToolUseID = ""
					cc.ToolResults[i] = r
				}
				uc.UserInputMessageContext = &cc
			}
			clone.UserInputMessage = &uc
		}
		if a := entry.AssistantResponseMessage; a != nil {
			ac := *a
			ac.ToolUses = make([]KiroToolUse, len(a.ToolUses))
			for i, tu := range a.ToolUses {
				tu.ToolUseID = ""
				ac.ToolUses[i] = tu
			}
			clone.AssistantResponseMessage = &ac
		}
		noID = append(noID, diagHash(clone))
	}
	return full, noID
}

// prefixDivergence compares the previous request's entries with the current
// ones. It returns "-" for the first request, "ok" when every previous entry
// is unchanged, "shorter@N" when the current request has fewer entries than
// before but matches up to N, or "@I:ids" / "@I:content" for the first changed
// index, where ids means only tool IDs differ there.
func prefixDivergence(prev, cur, prevNoID, curNoID []uint64, first bool) string {
	if first {
		return "-"
	}
	n := minInt(len(prev), len(cur))
	for i := 0; i < n; i++ {
		if prev[i] == cur[i] {
			continue
		}
		kind := "content"
		if i < len(prevNoID) && i < len(curNoID) && prevNoID[i] == curNoID[i] {
			kind = "ids"
		}
		return "@" + strconv.Itoa(i) + ":" + kind
	}
	if len(cur) < len(prev) {
		return "shorter@" + strconv.Itoa(n)
	}
	return "ok"
}

// observe logs one [CacheDiag] line comparing this request with the previous
// one of the same conversation, keyed by the Kiro conversation ID (derived
// from the system prompt and the first user message).
func (t *cacheDiagTracker) observe(req *ClaudeRequest, payload *KiroPayload) {
	if t == nil || req == nil || payload == nil {
		return
	}
	conv := payload.ConversationState.ConversationID
	client, clientNoID := clientMessageHashes(req.Messages)
	kiro, kiroNoID := kiroHistoryHashes(payload.ConversationState.History)
	var tools uint64
	if ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext; ctx != nil {
		tools = diagHash(ctx.Tools)
	}
	now := time.Now()

	t.mu.Lock()
	prev, ok := t.entries[conv]
	if ok && now.Sub(prev.seen) > cacheDiagTTL {
		ok = false
	}
	if !ok {
		prev = &cacheDiagEntry{}
	}
	clientDiv := prefixDivergence(prev.client, client, prev.clientNoID, clientNoID, !ok)
	kiroDiv := prefixDivergence(prev.kiro, kiro, prev.kiroNoID, kiroNoID, !ok)
	toolsState := "-"
	if ok {
		toolsState = "same"
		if prev.tools != tools {
			toolsState = "changed"
		}
	}
	prevClient, prevKiro := len(prev.client), len(prev.kiro)
	t.entries[conv] = &cacheDiagEntry{client: client, clientNoID: clientNoID, kiro: kiro, kiroNoID: kiroNoID, tools: tools, seen: now}
	if len(t.entries) > cacheDiagMaxEntries {
		for k, e := range t.entries {
			if now.Sub(e.seen) > cacheDiagTTL || k != conv {
				delete(t.entries, k)
				if len(t.entries) <= cacheDiagMaxEntries {
					break
				}
			}
		}
	}
	t.mu.Unlock()

	shrink := payload.Shrink.Stage
	if shrink == "" {
		shrink = "none"
	}
	convShort := conv
	if len(convShort) > 8 {
		convShort = convShort[:8]
	}
	logger.Infof("[CacheDiag] conv=%s msgs=%d prev=%d client=%s hist=%d prevHist=%d kiro=%s tools=%s shrink=%s dropped=%d bytes=%d",
		convShort, len(client), prevClient, clientDiv, len(kiro), prevKiro, kiroDiv, toolsState,
		shrink, payload.Shrink.DroppedTurns, payload.Shrink.OriginalBytes)
}
