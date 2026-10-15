package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"kiro-go/config"
	"net/http"
	"strings"
	"testing"
)

func TestNativeReasoningEfforts(t *testing.T) {
	cases := []struct {
		model string
		want  []string
	}{
		{"claude-opus-4.8", effortsWithXHigh},
		{"claude-opus-4.7", effortsWithXHigh},
		{"claude-opus-4.6", effortsWithoutXHigh},
		{"claude-sonnet-4.6", effortsWithoutXHigh},
		{"claude-opus-5.5", effortsWithXHigh},
		{"claude-sonnet-5", effortsWithXHigh},
		{"claude-haiku-5.5", effortsWithXHigh},
		{"claude-fable-5.1", effortsWithXHigh},
		{"claude-mythos-5", effortsWithXHigh},
		{"claude-sonnet-4.5", nil},
		{"claude-haiku-4.5", nil},
		{"claude-sonnet-4", nil},
		{"gpt-4o", nil},
	}
	for _, tc := range cases {
		got := nativeReasoningEfforts(tc.model)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("nativeReasoningEfforts(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestSelectReasoningEffort(t *testing.T) {
	enabled := func(budget int) *ClaudeThinkingConfig {
		return &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: budget}
	}
	cases := []struct {
		name      string
		supported []string
		explicit  string
		cfg       *ClaudeThinkingConfig
		want      string
	}{
		{"explicit wins", effortsWithXHigh, "low", enabled(60000), "low"},
		{"xhigh on 4.6 falls to strongest", effortsWithoutXHigh, "xhigh", nil, "max"},
		{"budget low", effortsWithXHigh, "", enabled(2000), "low"},
		{"budget medium", effortsWithXHigh, "", enabled(10000), "medium"},
		{"budget high", effortsWithXHigh, "", enabled(32000), "high"},
		{"budget huge", effortsWithXHigh, "", enabled(100000), "xhigh"},
		{"budget huge on 4.6", effortsWithoutXHigh, "", enabled(100000), "max"},
		{"adaptive defaults high", effortsWithXHigh, "", &ClaudeThinkingConfig{Type: "adaptive"}, "high"},
		{"suffix only defaults high", effortsWithXHigh, "", nil, "high"},
		{"unknown effort defaults high", effortsWithXHigh, "turbo", nil, "high"},
		{"openai minimal", effortsWithXHigh, "minimal", nil, "low"},
	}
	for _, tc := range cases {
		if got := selectReasoningEffort(tc.supported, tc.explicit, tc.cfg); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func effortOf(t *testing.T, fields map[string]interface{}) string {
	t.Helper()
	oc, ok := fields["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing output_config in %v", fields)
	}
	effort, _ := oc["effort"].(string)
	return effort
}

func TestClaudeToKiroNativeReasoningReplacesPromptTags(t *testing.T) {
	req := &ClaudeRequest{
		Model:        "claude-opus-4.8",
		MaxTokens:    32000,
		System:       "be brief",
		Thinking:     &ClaudeThinkingConfig{Type: "adaptive"},
		OutputConfig: &ClaudeOutputConfig{Effort: "xhigh"},
		Messages:     []ClaudeMessage{{Role: "user", Content: "hi"}},
	}

	payload := ClaudeToKiro(req, true)

	if got := effortOf(t, payload.AdditionalModelRequestFields); got != "xhigh" {
		t.Fatalf("effort = %q, want xhigh", got)
	}
	priming := payload.ConversationState.History[0].UserInputMessage.Content
	if strings.Contains(priming, "<thinking_mode>") {
		t.Fatalf("native reasoning models must not get prompt tags, got %q", priming)
	}
}

func TestClaudeToKiroLegacyThinkingKeepsPromptTagsWithBudget(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 16000,
		Thinking:  &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 8000},
		Messages:  []ClaudeMessage{{Role: "user", Content: "hi"}},
	}

	payload := ClaudeToKiro(req, true)

	if payload.AdditionalModelRequestFields != nil {
		t.Fatalf("legacy model must not get additionalModelRequestFields: %v", payload.AdditionalModelRequestFields)
	}
	priming := payload.ConversationState.History[0].UserInputMessage.Content
	if !strings.Contains(priming, "<max_thinking_length>8000</max_thinking_length>") {
		t.Fatalf("expected budget in thinking prompt, got %q", priming)
	}
}

func TestClaudeToKiroNativeReasoningRules(t *testing.T) {
	base := func() *ClaudeRequest {
		return &ClaudeRequest{Model: "claude-opus-4.7", MaxTokens: 1000, Messages: []ClaudeMessage{{Role: "user", Content: "hi"}}}
	}

	plain := ClaudeToKiro(base(), false)
	if plain.AdditionalModelRequestFields != nil {
		t.Fatalf("no thinking and no effort must send nothing, got %v", plain.AdditionalModelRequestFields)
	}

	effortOnly := base()
	effortOnly.OutputConfig = &ClaudeOutputConfig{Effort: "low"}
	if got := effortOf(t, ClaudeToKiro(effortOnly, false).AdditionalModelRequestFields); got != "low" {
		t.Fatalf("effort-only request: got %q", got)
	}

	disabled := base()
	disabled.Thinking = &ClaudeThinkingConfig{Type: "disabled"}
	disabled.OutputConfig = &ClaudeOutputConfig{Effort: "high"}
	if fields := ClaudeToKiro(disabled, false).AdditionalModelRequestFields; fields != nil {
		t.Fatalf("thinking disabled must send nothing, got %v", fields)
	}
}

func TestNativeReasoningSwitchOff(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	off := false
	if err := config.UpdateCompatSettings(config.CompatSettings{NativeReasoningEffort: &off}); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })

	req := &ClaudeRequest{Model: "claude-opus-4.8", MaxTokens: 1000, Messages: []ClaudeMessage{{Role: "user", Content: "hi"}}}
	payload := ClaudeToKiro(req, true)
	if payload.AdditionalModelRequestFields != nil {
		t.Fatalf("switch off must disable native fields, got %v", payload.AdditionalModelRequestFields)
	}
	if !strings.Contains(payload.ConversationState.History[0].UserInputMessage.Content, "<thinking_mode>") {
		t.Fatal("switch off must fall back to prompt tags")
	}
}

func TestOpenAIReasoningEffortMapsToOutputConfig(t *testing.T) {
	req := &OpenAIRequest{
		Model:           "claude-opus-4.6",
		ReasoningEffort: "medium",
		Messages:        []OpenAIMessage{{Role: "user", Content: "hi"}},
	}
	if got := effortOf(t, OpenAIToKiro(req, false).AdditionalModelRequestFields); got != "medium" {
		t.Fatalf("effort = %q, want medium", got)
	}
	req.ReasoningEffort = "none"
	if fields := OpenAIToKiro(req, false).AdditionalModelRequestFields; fields != nil {
		t.Fatalf("reasoning_effort none must send nothing, got %v", fields)
	}
}

func TestCallKiroAPIAddsIDEThinkingWrapperOnlyForOAuth(t *testing.T) {
	payload := newKiroRetryTestPayload()
	payload.AdditionalModelRequestFields = map[string]interface{}{
		"output_config": map[string]interface{}{"effort": "high"},
	}

	capture := func(account *config.Account) map[string]interface{} {
		var body []byte
		installKiroStreamTestClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ = io.ReadAll(req.Body)
			return kiroStreamTestResponse(bytes.NewReader(awsEventStreamFrame(t,
				"assistantResponseEvent", map[string]interface{}{"content": "ok"}))), nil
		}))
		if err := CallKiroAPI(account, payload, &KiroStreamCallback{}); err != nil {
			t.Fatalf("CallKiroAPI: %v", err)
		}
		var sent struct {
			Fields map[string]interface{} `json:"additionalModelRequestFields"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("decode body %s: %v", body, err)
		}
		return sent.Fields
	}

	installKiroRetryTestEndpoints(t)
	ide := capture(newKiroRetryTestOAuthAccount())
	thinkingWrapper, _ := ide["thinking"].(map[string]interface{})
	if thinkingWrapper["type"] != "adaptive" || thinkingWrapper["display"] != "summarized" {
		t.Fatalf("IDE request must carry the adaptive thinking wrapper, got %v", ide)
	}

	cli := capture(newKiroRetryTestAPIKeyAccount("us-east-1"))
	if _, ok := cli["thinking"]; ok {
		t.Fatalf("CLI request must keep the bare output_config form, got %v", cli)
	}
	if _, ok := payload.AdditionalModelRequestFields["thinking"]; ok {
		t.Fatal("endpoint adaptation must not mutate the shared payload")
	}
}
