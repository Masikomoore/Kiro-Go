package proxy

import (
	"bytes"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClaudeSSEWriterPingsImmediatelyAndOnInterval(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := newClaudeSSEWriter(rec, rec, 30*time.Millisecond)
	writer.Start()
	time.Sleep(80 * time.Millisecond)
	writer.Stop()
	time.Sleep(50 * time.Millisecond)

	body := rec.Body.String()
	count := strings.Count(body, "event: ping\n")
	if count < 2 {
		t.Fatalf("expected an immediate ping plus interval pings, got %d in %q", count, body)
	}
	if !strings.Contains(body, `data: {"type":"ping"}`) {
		t.Fatalf("ping payload missing: %s", body)
	}

	before := strings.Count(body, "event: ping\n")
	time.Sleep(50 * time.Millisecond)
	if got := strings.Count(rec.Body.String(), "event: ping\n"); got != before {
		t.Fatalf("ping continued after Stop: before=%d after=%d", before, got)
	}
}

func TestClaudeSSEWriterSendDoesNotSplitAFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := newClaudeSSEWriter(rec, rec, time.Hour)
	writer.Send("message_start", map[string]string{"type": "message_start"})
	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: message_start\n") || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("malformed sse frame: %q", body)
	}
}

func TestClaudeSSEWriterArmCanBeDisarmed(t *testing.T) {
	rec := httptest.NewRecorder()
	writer := newClaudeSSEWriter(rec, rec, 40*time.Millisecond)
	writer.Arm()
	writer.Disarm()
	time.Sleep(80 * time.Millisecond)
	if body := rec.Body.String(); body != "" {
		t.Fatalf("disarmed writer wrote %q", body)
	}
}

func TestFirstByteReaderNotifiesOnce(t *testing.T) {
	var calls int
	reader := &firstByteReader{
		r: io.MultiReader(bytes.NewReader(nil), bytes.NewReader([]byte("ab")), bytes.NewReader([]byte("cd"))),
		notify: func() {
			calls++
		},
	}
	buf := make([]byte, 2)
	if _, err := reader.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(buf); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("notify calls = %d, want 1", calls)
	}
}
