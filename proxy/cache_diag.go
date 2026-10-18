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
	clientParts [][]partSum
	kiroParts   [][]partSum
	client      []uint64
	clientNoID  []uint64
	kiro        []uint64
	kiroNoID    []uint64
	tools       uint64
	seen        time.Time
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

func diagHashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// clientMessageHashes hashes each client message in the cache tracker's
// canonical form, which ignores cache_control (Claude Code moves its marker to
// the newest message every turn). The second slice also drops tool IDs, so a
// divergence caused only by regenerated IDs is visible.
func clientMessageHashes(messages []ClaudeMessage) (full, noID []uint64) {
	for _, msg := range messages {
		full = append(full, diagHashString(canonicalizeCacheValue(map[string]interface{}{"role": msg.Role, "content": msg.Content})))
		content := msg.Content
		if blocks, ok := content.([]interface{}); ok {
			stripped := make([]interface{}, len(blocks))
			for i, block := range blocks {
				stripped[i] = stripVolatileToolIDs(block)
			}
			content = stripped
		}
		noID = append(noID, diagHashString(canonicalizeCacheValue(map[string]interface{}{"role": msg.Role, "content": content})))
	}
	return full, noID
}

// partSum summarizes one content block or one part of a Kiro entry, so a
// later request can name what changed without keeping the content.
type partSum struct {
	name string
	hash uint64
	size int
}

// clientBlockSums summarizes each content block of a client message in the
// tracker's canonical form.
func clientBlockSums(msg ClaudeMessage) []partSum {
	var blocks []interface{}
	switch c := msg.Content.(type) {
	case []interface{}:
		blocks = c
	default:
		blocks = []interface{}{map[string]interface{}{"type": "string", "text": c}}
	}
	out := make([]partSum, len(blocks))
	for i, block := range blocks {
		canon := canonicalizeCacheValue(block)
		typ := "-"
		if m, ok := block.(map[string]interface{}); ok {
			typ, _ = m["type"].(string)
		}
		out[i] = partSum{name: msg.Role + "." + typ, hash: diagHashString(canon), size: len(canon)}
	}
	return out
}

// kiroPartSums summarizes the parts of a Kiro history entry.
func kiroPartSums(entry KiroHistoryMessage) []partSum {
	sum := func(name string, v interface{}) partSum {
		raw, _ := json.Marshal(v)
		return partSum{name: name, hash: diagHashString(string(raw)), size: len(raw)}
	}
	if u := entry.UserInputMessage; u != nil {
		return []partSum{
			sum("user.content", u.Content),
			sum("user.images", u.Images),
			sum("user.context", u.UserInputMessageContext),
			sum("user.meta", []string{u.ModelID, u.Origin}),
		}
	}
	if a := entry.AssistantResponseMessage; a != nil {
		return []partSum{sum("assistant.content", a.Content), sum("assistant.toolUses", a.ToolUses)}
	}
	return nil
}

// firstPartDiff names the first differing part, e.g. "b1:user.tool_result
// 812>790" for client blocks or "assistant.content 1200>1180" for Kiro parts.
func firstPartDiff(prev, cur []partSum, indexed bool) string {
	n := maxInt(len(prev), len(cur))
	for i := 0; i < n; i++ {
		var p, c partSum
		if i < len(prev) {
			p = prev[i]
		}
		if i < len(cur) {
			c = cur[i]
		}
		if p.hash == c.hash && p.name == c.name {
			continue
		}
		name := c.name
		if name == "" {
			name = p.name
		}
		label := name
		if indexed {
			label = "b" + strconv.Itoa(i) + ":" + name
		}
		return label + " " + strconv.Itoa(p.size) + ">" + strconv.Itoa(c.size)
	}
	return "same-parts"
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
// divergenceIndex extracts I from "@I:kind", or -1.
func divergenceIndex(div string) int {
	if len(div) < 2 || div[0] != '@' {
		return -1
	}
	end := 1
	for end < len(div) && div[end] >= '0' && div[end] <= '9' {
		end++
	}
	i, err := strconv.Atoi(div[1:end])
	if err != nil {
		return -1
	}
	return i
}

func (t *cacheDiagTracker) observe(req *ClaudeRequest, payload *KiroPayload) {
	if t == nil || req == nil || payload == nil {
		return
	}
	conv := payload.ConversationState.ConversationID
	client, clientNoID := clientMessageHashes(req.Messages)
	kiro, kiroNoID := kiroHistoryHashes(payload.ConversationState.History)
	clientParts := make([][]partSum, len(req.Messages))
	for i, msg := range req.Messages {
		clientParts[i] = clientBlockSums(msg)
	}
	kiroParts := make([][]partSum, len(payload.ConversationState.History))
	for i, entry := range payload.ConversationState.History {
		kiroParts[i] = kiroPartSums(entry)
	}
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
	clientDetail, kiroDetail := "", ""
	if i := divergenceIndex(clientDiv); i >= 0 && i < len(prev.clientParts) && i < len(clientParts) {
		clientDetail = "[" + firstPartDiff(prev.clientParts[i], clientParts[i], true) + "]"
	}
	if i := divergenceIndex(kiroDiv); i >= 0 && i < len(prev.kiroParts) && i < len(kiroParts) {
		kiroDetail = "[" + firstPartDiff(prev.kiroParts[i], kiroParts[i], false) + "]"
	}
	t.entries[conv] = &cacheDiagEntry{
		clientParts: clientParts, kiroParts: kiroParts,
		client: client, clientNoID: clientNoID, kiro: kiro, kiroNoID: kiroNoID, tools: tools, seen: now,
	}
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
	convShort := shortConversationID(payload)
	logger.Infof("[CacheDiag] conv=%s msgs=%d prev=%d client=%s%s hist=%d prevHist=%d kiro=%s%s tools=%s shrink=%s dropped=%d bytes=%d",
		convShort, len(client), prevClient, clientDiv, clientDetail, len(kiro), prevKiro, kiroDiv, kiroDetail, toolsState,
		shrink, payload.Shrink.DroppedTurns, payload.Shrink.OriginalBytes)
}

// shortConversationID is the first 8 characters of the Kiro conversation ID,
// used to tie timing, cache, and diagnostic log lines to one conversation.
func shortConversationID(payload *KiroPayload) string {
	if payload == nil {
		return "-"
	}
	id := payload.ConversationState.ConversationID
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}
