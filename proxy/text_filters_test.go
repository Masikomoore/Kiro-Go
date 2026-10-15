package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func runXMLFilter(enabled bool, chunks ...string) string {
	f := newToolUseXMLFilter(enabled)
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(f.push(c))
	}
	out.WriteString(f.flush())
	return out.String()
}

func TestToolUseXMLFilter(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"plain text", []string{"hello ", "world"}, "hello world"},
		{"whole leak", []string{`before <tool_use name="Read">{"path":"a"}</tool_use> after`}, "before  after"},
		{"leak split across chunks", []string{"a <tool", "_use>{\"x\":1}</tool", "_use>b"}, "a b"},
		{"marker split before space", []string{"x <tool_use", ` id="1">y</tool_use>z`}, "x z"},
		{"unterminated leak dropped", []string{"keep <tool_use>never closed"}, "keep "},
		{"lookalike tag kept", []string{"see <tool_use_id> and <tool_used>"}, "see <tool_use_id> and <tool_used>"},
		{"inside code fence kept", []string{"```xml\n<tool_use>demo</tool_use>\n```\nafter"}, "```xml\n<tool_use>demo</tool_use>\n```\nafter"},
		{"fence split across chunks", []string{"``", "`\n<tool_use>demo</tool_use>\n``", "`"}, "```\n<tool_use>demo</tool_use>\n```"},
		{"after fence closes leak stripped", []string{"```\ncode\n```\n<tool_use>x</tool_use>end"}, "```\ncode\n```\nend"},
		{"partial marker at end flushed", []string{"ends with <tool_us"}, "ends with <tool_us"},
		{"unicode around leak", []string{"中文<tool_use>{}</tool_use>结束"}, "中文结束"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runXMLFilter(true, tc.chunks...); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := runXMLFilter(false, "a <tool_use>x</tool_use> b"); got != "a <tool_use>x</tool_use> b" {
		t.Fatalf("disabled filter must pass text through, got %q", got)
	}
}

func TestParseEventStreamStripsLeakedToolUseXML(t *testing.T) {
	var body bytes.Buffer
	body.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "Reading <tool_"}))
	body.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "use name=\"Read\">{}</tool_use> now"}))
	body.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))

	var text strings.Builder
	err := parseEventStream(&body, &KiroStreamCallback{
		OnText: func(chunk string, thinking bool) {
			if !thinking {
				text.WriteString(chunk)
			}
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := text.String(); got != "Reading  now" {
		t.Fatalf("text = %q", got)
	}
}
