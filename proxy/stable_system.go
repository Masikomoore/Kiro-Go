package proxy

import (
	"kiro-go/config"
	"kiro-go/logger"
	"strings"
	"sync"
	"time"
)

// Stable system prompt.
//
// Some clients change the last few KB of their system prompt on every turn
// (a counter, a clock, a growing list), while the rest of the conversation
// only grows. kiro-go sends the system prompt as Kiro history entry 0 and
// hashes it into the conversation ID, so each such turn got a new conversation
// ID and Kiro billed the whole prompt cold: on CA2, 370k–460k-token turns at
// 4.7–11 credits each instead of ~1.5. A direct test (findings 2026-10-26)
// kept the changing tail out of entry 0 and Kiro read the prefix again
// (1.46 → 0.40 credits per follow-up turn at 119k tokens).
//
// Per conversation (model + first user text), the last system prompt is
// remembered as chunk hashes. When a later one shares a long prefix and
// differs only in a short tail, that prefix (cut at a line break) is frozen:
// it becomes entry 0 and the conversation-ID seed, and the rest of the system
// prompt is appended to the current user message only, so older history
// entries stay byte-stable. If the tail outgrows stableSystemMaxTail or the
// prefix itself changes, the prefix is re-frozen from the last two prompts,
// which costs one cold turn.

const (
	stableSystemMinPrefix = 2048
	stableSystemMaxTail   = 32 * 1024
	stableSystemTTL       = 2 * time.Hour
	stableSystemMax       = 2048
)

type stableSystemEntry struct {
	chunks     []uint64 // seedChunkBytes hashes of the last system prompt seen
	length     int
	prefixLen  int // 0 until a volatile tail was found
	prefixHash uint64
	seen       time.Time
}

var (
	stableSystemsMu sync.Mutex
	stableSystems   = make(map[uint64]*stableSystemEntry)
)

// splitStableSystem returns the part of system to keep in history entry 0 and
// the volatile tail to send with the current message ("" when nothing moves).
func splitStableSystem(model, anchor, system string) (stable, tail string) {
	if !config.GetCompatConfig().StableSystemPrompt || system == "" || isSyntheticConversationAnchor(anchor) {
		return system, ""
	}
	key := diagHashString(model + "\n" + strings.TrimSpace(anchor))
	chunks := chunkHashes(system)
	now := time.Now()

	stableSystemsMu.Lock()
	defer stableSystemsMu.Unlock()
	e := stableSystems[key]
	if e != nil && now.Sub(e.seen) > stableSystemTTL {
		e = nil
	}
	defer func() { rememberStableSystem(key, e, chunks, len(system), now) }()

	// Frozen prefix still matches and the tail is still short.
	if e != nil && e.prefixLen > 0 && len(system) >= e.prefixLen && len(system)-e.prefixLen <= stableSystemMaxTail &&
		diagHashString(system[:e.prefixLen]) == e.prefixHash {
		return system[:e.prefixLen], strings.TrimSpace(system[e.prefixLen:])
	}
	if e == nil || (e.length == len(system) && equalHashes(e.chunks, chunks)) {
		if e != nil {
			e.prefixLen = 0
		}
		return system, ""
	}

	// The prompt changed: freeze the shared prefix when only the tail differs.
	same := 0
	for same < len(e.chunks) && same < len(chunks) && e.chunks[same] == chunks[same] {
		same++
	}
	shared := minInt(same*seedChunkBytes, minInt(len(system), e.length))
	n := strings.LastIndexByte(system[:shared], '\n') + 1
	if n < stableSystemMinPrefix || len(system)-n > stableSystemMaxTail || e.length-n > stableSystemMaxTail {
		e.prefixLen = 0
		return system, ""
	}
	e.prefixLen, e.prefixHash = n, diagHashString(system[:n])
	logger.Infof("[StableSystem] model=%s prefix=%d tail=%d prevLen=%d", model, n, len(system)-n, e.length)
	return system[:n], strings.TrimSpace(system[n:])
}

// rememberStableSystem stores the latest prompt of a conversation and keeps
// the map bounded. Called with stableSystemsMu held.
func rememberStableSystem(key uint64, e *stableSystemEntry, chunks []uint64, length int, now time.Time) {
	if e == nil {
		e = &stableSystemEntry{}
	}
	e.chunks, e.length, e.seen = chunks, length, now
	stableSystems[key] = e
	if len(stableSystems) <= stableSystemMax {
		return
	}
	for k, old := range stableSystems {
		if now.Sub(old.seen) > stableSystemTTL || k != key {
			delete(stableSystems, k)
			if len(stableSystems) <= stableSystemMax {
				return
			}
		}
	}
}

func equalHashes(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// withSystemTail appends the volatile end of the system prompt to the current
// user content, marked so the model reads it as system instructions.
func withSystemTail(content, tail string) string {
	if tail == "" {
		return content
	}
	return joinHistoryText(content, "<system-prompt-continued>\n"+tail+"\n</system-prompt-continued>")
}
