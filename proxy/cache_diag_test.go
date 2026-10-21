package proxy

import (
	"fmt"
	"kiro-go/config"
	"strings"
	"testing"
)

func TestPrefixDivergence(t *testing.T) {
	cases := []struct {
		prev, cur, prevNoID, curNoID []uint64
		first                        bool
		want                         string
	}{
		{nil, []uint64{1}, nil, []uint64{1}, true, "-"},
		{[]uint64{1, 2}, []uint64{1, 2, 3}, []uint64{1, 2}, []uint64{1, 2, 3}, false, "ok"},
		{[]uint64{1, 2}, []uint64{1, 9, 3}, []uint64{1, 2}, []uint64{1, 2, 3}, false, "@1:ids"},
		{[]uint64{1, 2}, []uint64{1, 9, 3}, []uint64{1, 2}, []uint64{1, 8, 3}, false, "@1:content"},
		{[]uint64{1, 2, 3}, []uint64{1, 2}, []uint64{1, 2, 3}, []uint64{1, 2}, false, "shorter@2"},
	}
	for _, c := range cases {
		if got := prefixDivergence(c.prev, c.cur, c.prevNoID, c.curNoID, c.first); got != c.want {
			t.Errorf("prefixDivergence(%v, %v) = %q, want %q", c.prev, c.cur, got, c.want)
		}
	}
}

// bigConversation builds a conversation whose Kiro payload exceeds
// maxPayloadBytes(), like the ~920 KB requests seen on CA2.
func bigConversation(turns int) *ClaudeRequest {
	msgs := []ClaudeMessage{}
	for i := 0; i < turns; i++ {
		msgs = append(msgs, ClaudeMessage{Role: "user", Content: fmt.Sprintf("turn %d: %s", i, strings.Repeat("read the file and explain it. ", 1200))})
		if i < turns-1 {
			msgs = append(msgs, ClaudeMessage{Role: "assistant", Content: fmt.Sprintf("answer %d: %s", i, strings.Repeat("the file defines a handler. ", 1200))})
		}
	}
	return &ClaudeRequest{Model: "claude-opus-5.5", System: "You are a coding assistant.", Messages: msgs}
}

func resetTruncationBoundaries(t *testing.T) {
	t.Helper()
	truncationBoundariesMu.Lock()
	truncationBoundaries = make(map[string]truncationBoundary)
	truncationBoundariesMu.Unlock()
	t.Cleanup(func() {
		truncationBoundariesMu.Lock()
		truncationBoundaries = make(map[string]truncationBoundary)
		truncationBoundariesMu.Unlock()
	})
}

// Dropping the oldest turns used to move the cut on every turn, changing the
// Kiro history right after the system priming so upstream could not reuse its
// cached prefix. The boundary now stays put while the payload fits.
func TestTruncationKeepsKiroHistoryPrefixStable(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	resetTruncationBoundaries(t)
	first := ClaudeToKiro(bigConversation(30), false)
	second := ClaudeToKiro(bigConversation(31), false)
	if first.Shrink.Stage != "drop" || second.Shrink.Stage != "drop" {
		t.Fatalf("expected both payloads to drop turns, got %+v and %+v", first.Shrink, second.Shrink)
	}
	if first.ConversationState.ConversationID != second.ConversationState.ConversationID {
		t.Fatal("both turns must share a conversation ID")
	}
	k1, k1n := kiroHistoryHashes(first.ConversationState.History)
	k2, k2n := kiroHistoryHashes(second.ConversationState.History)
	if got := prefixDivergence(k1, k2, k1n, k2n, false); got != "ok" {
		t.Fatalf("the Kiro history prefix must stay stable, got %q (dropped %d then %d)", got, first.Shrink.DroppedTurns, second.Shrink.DroppedTurns)
	}
	if size := payloadByteSize(first); size > int(float64(maxPayloadBytes())*truncationTargetFraction)+64*1024 {
		t.Fatalf("a new boundary should leave headroom, payload is %d bytes", size)
	}
}

// Once the kept suffix no longer fits, the boundary moves once and then holds.
func TestTruncationBoundaryMovesOnlyWhenFull(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	resetTruncationBoundaries(t)
	moves := 0
	var prev []uint64
	var prevNoID []uint64
	for turns := 30; turns <= 60; turns++ {
		p := ClaudeToKiro(bigConversation(turns), false)
		cur, curNoID := kiroHistoryHashes(p.ConversationState.History)
		if prev != nil && prefixDivergence(prev, cur, prevNoID, curNoID, false) != "ok" {
			moves++
		}
		if size := payloadByteSize(p); size > maxPayloadBytes() {
			t.Fatalf("turn %d payload %d bytes exceeds the limit", turns, size)
		}
		prev, prevNoID = cur, curNoID
	}
	t.Logf("boundary moved %d times over 31 turns", moves)
	if moves == 0 || moves > 6 {
		t.Fatalf("expected a few boundary moves over 30 growing turns, got %d", moves)
	}
}

// Claude Code moves its cache_control marker to the newest message every
// turn; that must not read as a client-side change.
func TestClientHashesIgnoreMovingCacheControl(t *testing.T) {
	block := func(text string, marked bool) interface{} {
		b := map[string]interface{}{"type": "text", "text": text}
		if marked {
			b["cache_control"] = map[string]interface{}{"type": "ephemeral"}
		}
		return b
	}
	prev := []ClaudeMessage{{Role: "user", Content: []interface{}{block("first", true)}}}
	cur := []ClaudeMessage{
		{Role: "user", Content: []interface{}{block("first", false)}},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: []interface{}{block("second", true)}},
	}
	p, pn := clientMessageHashes(prev)
	c, cn := clientMessageHashes(cur)
	if got := prefixDivergence(p, c, pn, cn, false); got != "ok" {
		t.Fatalf("moving cache_control must not count as a change, got %q", got)
	}
}

func TestFirstPartDiffNamesTheChangedBlock(t *testing.T) {
	prev := clientBlockSums(ClaudeMessage{Role: "user", Content: []interface{}{
		map[string]interface{}{"type": "text", "text": "same"},
		map[string]interface{}{"type": "tool_result", "tool_use_id": "a", "content": "long output"},
	}})
	cur := clientBlockSums(ClaudeMessage{Role: "user", Content: []interface{}{
		map[string]interface{}{"type": "text", "text": "same"},
		map[string]interface{}{"type": "tool_result", "tool_use_id": "a", "content": "short"},
	}})
	if got := firstPartDiff(prev, cur, true); !strings.HasPrefix(got, "b1:user.tool_result ") {
		t.Fatalf("expected block 1 tool_result, got %q", got)
	}
}
