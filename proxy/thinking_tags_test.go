package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThinkingTagScannerFindStart(t *testing.T) {
	cases := []struct {
		name  string
		buf   string
		final bool
		want  int
	}{
		{"leading tag", "<thinking>\nplan", false, 0},
		{"backtick quoted", "use `<thinking>` here", true, -1},
		{"double quoted", `the "<thinking>" tag`, true, -1},
		{"inside fence", "```xml\n<thinking>\n```", true, -1},
		{"after fence closes", "```\ncode\n```\n<thinking>x", false, 13},
		{"tag at buffer end waits", "intro <thinking>", false, -1},
		{"tag at end when final", "intro <thinking>", true, 6},
		{"quoted then real", "`<thinking>` then <thinking>go", false, 18},
	}
	for _, tc := range cases {
		var s thinkingTagScanner
		if got := s.findStart(tc.buf, tc.final); got != tc.want {
			t.Errorf("%s: findStart(%q) = %d, want %d", tc.name, tc.buf, got, tc.want)
		}
	}

	var s thinkingTagScanner
	s.emitted("```go\n")
	if got := s.findStart("<thinking>\n", false); got != -1 {
		t.Fatalf("a fence opened in earlier text must hide the tag, got %d", got)
	}
}

func TestExtractThinkingFromContentSkipsQuotedTags(t *testing.T) {
	cases := []struct {
		in, answer, reasoning string
	}{
		{"<thinking>plan</thinking>\n\nanswer", "answer", "plan"},
		{"Use the `<thinking>` tag.", "Use the `<thinking>` tag.", ""},
		{"```\n<thinking>demo</thinking>\n```", "```\n<thinking>demo</thinking>\n```", ""},
		{"<thinking>mention `</thinking>` here</thinking>done", "done", "mention `</thinking>` here"},
		{"<thinking>never closed", "<thinking>never closed", ""},
	}
	for _, tc := range cases {
		answer, reasoning := extractThinkingFromContent(tc.in)
		if answer != tc.answer || reasoning != tc.reasoning {
			t.Errorf("extract(%q) = (%q, %q), want (%q, %q)", tc.in, answer, reasoning, tc.answer, tc.reasoning)
		}
	}
}

func TestClaudeStreamKeepsQuotedThinkingTagInAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "Wrap your plan in a `<thinking>` tag, then answer below the line."}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupIntegrityPathTest(t, server)

	body := postClaudeHello(t, h)

	if strings.Contains(body, "thinking_delta") {
		t.Fatalf("a quoted tag must not open a thinking block: %s", body)
	}
	if answer := claudeStreamAnswerText(t, body); answer != "Wrap your plan in a `<thinking>` tag, then answer below the line." {
		t.Fatalf("answer text must be kept intact, got %q from %s", answer, body)
	}
}

// claudeStreamAnswerText joins the text_delta payloads of a Claude SSE body.
// Answer text may be split across deltas at any byte boundary.
func claudeStreamAnswerText(t *testing.T, body string) string {
	t.Helper()
	var out strings.Builder
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event struct {
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("decode SSE data %q: %v", data, err)
		}
		if event.Delta.Type == "text_delta" {
			out.WriteString(event.Delta.Text)
		}
	}
	return out.String()
}

func TestAnswerFlushLenHoldsOnlyATagLength(t *testing.T) {
	cases := []struct {
		name  string
		buf   string
		final bool
		want  int
	}{
		{"short chunk waits", "Hello", false, 0},
		{"long chunk keeps tag length", strings.Repeat("a", 30), false, 30 - len(thinkingOpenTag)},
		{"final flushes all", "Hello", true, 5},
		// "é" is two bytes; the cut must not land inside it.
		{"rune boundary", "abcdeé" + strings.Repeat("x", 9), false, 5},
	}
	for _, tc := range cases {
		if got := answerFlushLen(tc.buf, tc.final); got != tc.want {
			t.Errorf("%s: answerFlushLen(%q) = %d, want %d", tc.name, tc.buf, got, tc.want)
		}
	}
}

// The quote before a tag can arrive in an earlier chunk than the tag itself.
// The held bytes must keep it so the tag is still recognized as quoted.
func TestClaudeStreamKeepsQuotedTagSplitAcrossChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "Wrap your whole plan in a `"}))
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "<thinking>` tag."}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupIntegrityPathTest(t, server)

	body := postClaudeHello(t, h)
	if strings.Contains(body, "thinking_delta") {
		t.Fatalf("a quoted tag must not open a thinking block: %s", body)
	}
	if answer := claudeStreamAnswerText(t, body); answer != "Wrap your whole plan in a `<thinking>` tag." {
		t.Fatalf("answer text must be kept intact, got %q", answer)
	}
}
