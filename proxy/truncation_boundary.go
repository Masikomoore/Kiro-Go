package proxy

import (
	"encoding/json"
	"sync"
	"time"
)

// Stable truncation boundary.
//
// Kiro caches prompt prefixes per account. truncatePayloadToLimit used to keep
// the largest suffix that fit, so every turn of a conversation over the limit
// dropped one or two more old turns and the history right after the system
// priming changed. On CA2 every such turn of a ~1.5 MB WorkBuddy conversation
// was billed at full price with an ~8 s first byte, while turns that happened
// to keep the same boundary cost about a third and started in 2–5 s.
//
// The boundary is now remembered per conversation by the hash of the first
// kept entry. A later turn reuses it while the payload still fits; otherwise
// it drops down to truncationTargetFraction of the limit, which leaves room
// for many turns before the next move. This follows the common advice for
// cache-friendly context management: keep the head byte-stable and move the
// cut rarely (e.g. arXiv 2604.03515).

const (
	// truncationTargetFraction is the share of maxPayloadBytes() a new boundary
	// aims for, leaving headroom so the boundary can stay put for a while.
	truncationTargetFraction = 0.8
	truncationBoundaryTTL    = 2 * time.Hour
	truncationBoundaryMax    = 2048
)

type truncationBoundary struct {
	firstKept uint64
	seen      time.Time
}

var (
	truncationBoundariesMu sync.Mutex
	truncationBoundaries   = make(map[string]truncationBoundary)
)

func historyEntryHash(entry KiroHistoryMessage) uint64 {
	raw, err := json.Marshal(entry)
	if err != nil {
		return 0
	}
	return diagHashString(string(raw))
}

// stableTruncationStart returns the index of the first conversation entry to
// keep. It reuses the conversation's previous boundary when that entry is still
// present and the kept suffix fits; otherwise it picks a new boundary that fits
// truncationTargetFraction of the limit. At least minRecentHistoryTurns entries
// are always kept.
func stableTruncationStart(conversationID string, conversation []KiroHistoryMessage, entrySizes []int, baseSize int) int {
	now := time.Now()
	if conversationID != "" {
		truncationBoundariesMu.Lock()
		prev, ok := truncationBoundaries[conversationID]
		truncationBoundariesMu.Unlock()
		if ok && now.Sub(prev.seen) <= truncationBoundaryTTL {
			for i := range conversation {
				if historyEntryHash(conversation[i]) != prev.firstKept {
					continue
				}
				size := baseSize
				for _, s := range entrySizes[i:] {
					size += s
				}
				if size <= maxPayloadBytes() || len(conversation)-i <= minRecentHistoryTurns {
					rememberTruncationBoundary(conversationID, prev.firstKept, now)
					return i
				}
				break
			}
		}
	}

	target := int(float64(maxPayloadBytes()) * truncationTargetFraction)
	keepFrom := len(conversation)
	running := baseSize
	for i := len(conversation) - 1; i >= 0; i-- {
		running += entrySizes[i]
		kept := len(conversation) - i
		if running > target && kept > minRecentHistoryTurns {
			break
		}
		keepFrom = i
	}
	if conversationID != "" && keepFrom < len(conversation) {
		rememberTruncationBoundary(conversationID, historyEntryHash(conversation[keepFrom]), now)
	}
	return keepFrom
}

func rememberTruncationBoundary(conversationID string, firstKept uint64, now time.Time) {
	truncationBoundariesMu.Lock()
	defer truncationBoundariesMu.Unlock()
	truncationBoundaries[conversationID] = truncationBoundary{firstKept: firstKept, seen: now}
	if len(truncationBoundaries) <= truncationBoundaryMax {
		return
	}
	for id, b := range truncationBoundaries {
		if now.Sub(b.seen) > truncationBoundaryTTL || id != conversationID {
			delete(truncationBoundaries, id)
			if len(truncationBoundaries) <= truncationBoundaryMax {
				return
			}
		}
	}
}
