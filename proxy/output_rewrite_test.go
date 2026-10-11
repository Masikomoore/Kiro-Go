package proxy

import (
	"bytes"
	"strings"
	"testing"

	"kiro-go/config"
)

func TestOutputRewriterReplacesKiroInAssistantText(t *testing.T) {
	rw := newOutputRewriter([]config.PromptFilterRule{{
		Type:    "regex",
		Match:   "(?i)kiro",
		Replace: "Claude",
		Enabled: true,
	}})

	var got strings.Builder
	for _, chunk := range []string{"我是 K", "iro,一个 AI 开发环境助手。"} {
		got.WriteString(rw.push(chunk, false))
	}
	got.WriteString(rw.flush(false))

	want := "我是 Claude,一个 AI 开发环境助手。"
	if got.String() != want {
		t.Fatalf("got %q, want %q", got.String(), want)
	}
}

func TestOutputRewriterKeepsUnrelatedText(t *testing.T) {
	rw := newOutputRewriter([]config.PromptFilterRule{{
		Type:    "regex",
		Match:   "(?i)kiro",
		Replace: "Claude",
		Enabled: true,
	}})

	got := rw.push("Kind red", false) + rw.flush(false)
	if got != "Kind red" {
		t.Fatalf("got %q", got)
	}
	if tail := rw.push("Kir", false) + rw.flush(false); tail != "Kir" {
		t.Fatalf("incomplete tail got %q", tail)
	}
}

func TestOutputRewriterPassesThroughWhenNoRegexRules(t *testing.T) {
	rw := newOutputRewriter(nil)
	if got := rw.push("我是 Kiro", false); got != "我是 Kiro" {
		t.Fatalf("got %q", got)
	}
}

func TestParseEventStreamRewritesSplitKiro(t *testing.T) {
	previous := outputRewriteRulesFn
	outputRewriteRulesFn = func() []config.PromptFilterRule {
		return []config.PromptFilterRule{{
			Type:    "regex",
			Match:   "(?i)kiro",
			Replace: "Claude",
			Enabled: true,
		}}
	}
	t.Cleanup(func() { outputRewriteRulesFn = previous })

	var stream bytes.Buffer
	stream.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "我是 K"}))
	stream.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "iro"}))

	var got strings.Builder
	_, err := parseEventStreamTracked(&stream, &KiroStreamCallback{
		OnText: func(text string, thinking bool) {
			if thinking {
				t.Fatalf("unexpected thinking %q", text)
			}
			got.WriteString(text)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "我是 Claude" {
		t.Fatalf("got %q", got.String())
	}
}
