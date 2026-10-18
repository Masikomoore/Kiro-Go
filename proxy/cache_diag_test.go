package proxy

import (
	"fmt"
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
// maxPayloadBytes, like the ~920 KB requests seen on CA2.
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

// Dropping the oldest turns to fit maxPayloadBytes changes the Kiro history
// right after the system priming on every turn, so upstream cannot reuse its
// cached prefix even though the client history only grew.
func TestTruncationBreaksKiroHistoryPrefix(t *testing.T) {
	first := ClaudeToKiro(bigConversation(30), false)
	second := ClaudeToKiro(bigConversation(31), false)
	if first.Shrink.Stage != "drop" || second.Shrink.Stage != "drop" {
		t.Fatalf("expected both payloads to drop turns, got %+v and %+v", first.Shrink, second.Shrink)
	}
	c1, c1n := clientMessageHashes(bigConversation(30).Messages)
	c2, c2n := clientMessageHashes(bigConversation(31).Messages)
	if got := prefixDivergence(c1, c2, c1n, c2n, false); got != "ok" {
		t.Fatalf("client history only grew, got %q", got)
	}
	k1, k1n := kiroHistoryHashes(first.ConversationState.History)
	k2, k2n := kiroHistoryHashes(second.ConversationState.History)
	got := prefixDivergence(k1, k2, k1n, k2n, false)
	if got == "ok" {
		t.Fatalf("expected the Kiro history prefix to change after truncation")
	}
	t.Logf("client=ok kiro=%s dropped %d then %d turns", got, first.Shrink.DroppedTurns, second.Shrink.DroppedTurns)
}
