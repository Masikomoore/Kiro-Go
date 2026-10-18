package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestUpstreamUsageLogRecordsTokenUsage(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "secret answer text"}))
	stream.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{
		"stopReason": "end_turn",
		"tokenUsage": map[string]interface{}{
			"uncachedInputTokens":  200,
			"cacheReadInputTokens": 9000,
			"outputTokens":         80,
		},
	}))
	stream.Write(awsEventStreamFrame(t, "meteringEvent", map[string]interface{}{"unit": "credit", "usage": 0.42}))

	usage := newUpstreamUsageLog()
	if _, err := parseEventStreamTracked(&stream, &KiroStreamCallback{OnRawEvent: usage.observe}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	line := usage.line("abcd1234", "Kiro IDE")
	for _, want := range []string{
		"events=assistantResponseEvent:1,metadataEvent:1,meteringEvent:1",
		"metadataEvent.tokenUsage.cacheReadInputTokens=9000",
		"metadataEvent.tokenUsage.uncachedInputTokens=200",
		"metadataEvent.stopReason=end_turn",
		"meteringEvent.usage=0.42",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %s", want, line)
		}
	}
	if strings.Contains(line, "secret answer text") {
		t.Fatalf("answer text leaked into the log: %s", line)
	}
}
