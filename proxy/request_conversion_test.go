package proxy

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func floatPtr(v float64) *float64 { return &v }

func TestClaudeToKiroFoldsTrailingSystemMessage(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		Messages: []ClaudeMessage{
			{Role: "user", Content: "real question"},
			{Role: "system", Content: "mid-conversation instruction"},
		},
	}

	payload := ClaudeToKiro(req, false)

	current := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	if !strings.Contains(current, "real question") {
		t.Fatalf("current message must stay the user's question, got %q", current)
	}
	history := payload.ConversationState.History
	if len(history) == 0 || history[0].UserInputMessage == nil {
		t.Fatalf("expected system priming in history, got %+v", history)
	}
	if !strings.Contains(history[0].UserInputMessage.Content, "mid-conversation instruction") {
		t.Fatalf("inline system text must reach the system prompt, got %q", history[0].UserInputMessage.Content)
	}
}

func TestClaudeToKiroFoldsMidConversationSystemKeepsAlternation(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1024,
		System:    []interface{}{map[string]interface{}{"type": "text", "text": "base system"}},
		Messages: []ClaudeMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "answer"},
			{Role: "system", Content: []interface{}{map[string]interface{}{"type": "text", "text": "late rule"}}},
			{Role: "user", Content: "second"},
		},
	}

	payload := ClaudeToKiro(req, false)

	priming := payload.ConversationState.History[0].UserInputMessage.Content
	if !strings.Contains(priming, "base system") || !strings.Contains(priming, "late rule") {
		t.Fatalf("system prompt must keep base and inline text, got %q", priming)
	}
	for i, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil && strings.Contains(h.UserInputMessage.Content, "late rule") && i != 0 {
			t.Fatalf("inline system text must not appear as a user turn at index %d", i)
		}
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "second" {
		t.Fatalf("current message = %q, want %q", got, "second")
	}
}

func TestValidateClaudeRequestShapeSkipsSystemRole(t *testing.T) {
	ok := &ClaudeRequest{Messages: []ClaudeMessage{
		{Role: "user", Content: "hi"},
		{Role: "system", Content: "rule"},
	}}
	if msg := validateClaudeRequestShape(ok); msg != "" {
		t.Fatalf("trailing system after user must pass, got %q", msg)
	}

	prefill := &ClaudeRequest{Messages: []ClaudeMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "partial"},
		{Role: "system", Content: "rule"},
	}}
	if msg := validateClaudeRequestShape(prefill); !strings.Contains(msg, "assistant-prefill") {
		t.Fatalf("assistant before a trailing system is still a prefill, got %q", msg)
	}
}

func TestBuildInferenceConfig(t *testing.T) {
	cases := []struct {
		name      string
		maxTokens int
		temp      *float64
		topP      *float64
		wantNil   bool
		wantTemp  *float64
		wantTopP  *float64
	}{
		{name: "nothing set", wantNil: true},
		{name: "zero temperature kept", temp: floatPtr(0), wantTemp: floatPtr(0)},
		{name: "both set drops top_p", temp: floatPtr(0.7), topP: floatPtr(0.9), wantTemp: floatPtr(0.7)},
		{name: "top_p alone kept", topP: floatPtr(0.9), wantTopP: floatPtr(0.9)},
		{name: "temperature clamped", temp: floatPtr(1.5), wantTemp: floatPtr(1)},
		{name: "negative top_p clamped", topP: floatPtr(-1), wantTopP: floatPtr(0)},
		{name: "max tokens only", maxTokens: 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := buildInferenceConfig(tc.maxTokens, tc.temp, tc.topP)
			if tc.wantNil {
				if cfg != nil {
					t.Fatalf("expected nil config, got %+v", cfg)
				}
				return
			}
			if cfg == nil {
				t.Fatal("expected config")
			}
			if cfg.MaxTokens != tc.maxTokens {
				t.Fatalf("maxTokens = %d, want %d", cfg.MaxTokens, tc.maxTokens)
			}
			assertFloatPtr(t, "temperature", cfg.Temperature, tc.wantTemp)
			assertFloatPtr(t, "topP", cfg.TopP, tc.wantTopP)
		})
	}
}

func assertFloatPtr(t *testing.T, name string, got, want *float64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Fatalf("%s = %v, want %v", name, got, want)
	case *got != *want:
		t.Fatalf("%s = %v, want %v", name, *got, *want)
	}
}

func TestClaudeToKiroSendsZeroTemperature(t *testing.T) {
	var req ClaudeRequest
	body := `{"model":"claude-sonnet-4.5","max_tokens":64,"temperature":0,"top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}

	payload := ClaudeToKiro(&req, false)
	raw, _ := json.Marshal(payload.InferenceConfig)
	if !strings.Contains(string(raw), `"temperature":0`) {
		t.Fatalf("temperature 0 must be sent, got %s", raw)
	}
	if strings.Contains(string(raw), "topP") {
		t.Fatalf("top_p must be dropped when temperature is set, got %s", raw)
	}
}

func TestLimitToolDescriptionKeepsValidUTF8(t *testing.T) {
	desc := strings.Repeat("工", maxToolDescLen)
	got := limitToolDescription(desc)
	if !utf8.ValidString(got) {
		t.Fatal("truncated description must be valid UTF-8")
	}
	if len(got) > maxToolDescLen+len("...") {
		t.Fatalf("truncated description too long: %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatal("truncated description must end with an ellipsis")
	}
	if short := "short"; limitToolDescription(short) != short {
		t.Fatal("short description must pass through")
	}
}
