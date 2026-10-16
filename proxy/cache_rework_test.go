package proxy

import (
	"encoding/base64"
	"encoding/json"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cachedSystemRequest(model string, extra ...ClaudeMessage) *ClaudeRequest {
	system := strings.Repeat("You are a careful assistant that follows the repository conventions. ", 120)
	msgs := append([]ClaudeMessage{{Role: "user", Content: "hello"}}, extra...)
	return &ClaudeRequest{
		Model: model,
		System: []interface{}{map[string]interface{}{
			"type": "text", "text": system,
			"cache_control": map[string]interface{}{"type": "ephemeral"},
		}},
		Messages: msgs,
	}
}

func useCompat(t *testing.T, s config.CompatSettings) {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.UpdateCompatSettings(s); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })
}

func TestPromptCacheSplitConservesTotals(t *testing.T) {
	cases := []promptCacheUsage{
		{CacheCreationInputTokens: 300, CacheReadInputTokens: 700, CacheCreation5mInputTokens: 100, CacheCreation1hInputTokens: 200, PromptTotalTokens: 1200},
		{CacheCreationInputTokens: 50000, CacheReadInputTokens: 0, CacheCreation5mInputTokens: 50000, PromptTotalTokens: 10},
		{CacheReadInputTokens: 999, PromptTotalTokens: 1000},
		{},
	}
	for _, u := range cases {
		for _, total := range []int{0, 1, 137, 5000, 150000} {
			s := u.split(total)
			if s.Input+s.Creation+s.Read != total {
				t.Fatalf("split(%d) of %+v = %+v does not add up", total, u, s)
			}
			if s.Creation5m+s.Creation1h != s.Creation {
				t.Fatalf("TTL breakdown %+v does not add up to creation", s)
			}
			if s.Input < 0 || s.Creation < 0 || s.Read < 0 {
				t.Fatalf("negative split %+v", s)
			}
		}
	}
}

func TestClaudeCodeSessionID(t *testing.T) {
	cases := map[string]string{
		`{"device_id":"d","account_uuid":"a","session_id":"0b4445e1-f5be-49e1-87ce-62bbc28ad705"}`: "0b4445e1-f5be-49e1-87ce-62bbc28ad705",
		"user_abc_account__session_0b4445e1-f5be-49e1-87ce-62bbc28ad705":                           "0b4445e1-f5be-49e1-87ce-62bbc28ad705",
		"plain-user":        "",
		`{"device_id":"d"}`: "",
	}
	for in, want := range cases {
		if got := claudeCodeSessionID(in); got != want {
			t.Errorf("claudeCodeSessionID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptCacheScopePrecedence(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	req := &ClaudeRequest{Metadata: &ClaudeMetadata{UserID: `{"session_id":"s1"}`}}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	r.Header.Set("X-Claude-Code-Session-Id", "s2")

	if got := promptCacheScope(req, r, "k1"); got != "sess:s1" {
		t.Fatalf("metadata session must win, got %q", got)
	}
	if got := promptCacheScope(&ClaudeRequest{}, r, "k1"); got != "sess:s2" {
		t.Fatalf("header session comes next, got %q", got)
	}
	if got := promptCacheScope(&ClaudeRequest{}, httptest.NewRequest(http.MethodPost, "/", nil), "k1"); got != "key:k1" {
		t.Fatalf("API key comes next, got %q", got)
	}
	if got := promptCacheScope(&ClaudeRequest{}, nil, ""); got != "" {
		t.Fatalf("no identity means account scope, got %q", got)
	}

	_ = config.UpdateCompatSettings(config.CompatSettings{PromptCacheScope: config.PromptCacheScopeAccount})
	if got := promptCacheScope(req, r, "k1"); got != "" {
		t.Fatalf("account scope setting must ignore sessions, got %q", got)
	}
}

func TestPromptCacheSessionScopeHitsAcrossAccounts(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0)
	profile.Scope = "sess:abc"

	if first := tracker.Compute("acct-a", profile); first.CacheReadInputTokens != 0 || first.CacheCreationInputTokens == 0 {
		t.Fatalf("first request must create, got %+v", first)
	}
	tracker.Update("acct-a", profile)

	second := tracker.Compute("acct-b", profile)
	if second.CacheReadInputTokens == 0 || second.CacheCreationInputTokens != 0 {
		t.Fatalf("same session on another account must read the whole prefix, got %+v", second)
	}
	last := profile.Breakpoints[len(profile.Breakpoints)-1].CumulativeTokens
	if second.CacheReadInputTokens != last {
		t.Fatalf("read = %d, want the full prefix %d (no 85%% cap)", second.CacheReadInputTokens, last)
	}

	other := *profile
	other.Scope = "sess:other"
	if got := tracker.Compute("acct-a", &other); got.CacheReadInputTokens != 0 {
		t.Fatalf("another session must not hit, got %+v", got)
	}

	accountScoped := *profile
	accountScoped.Scope = ""
	tracker.Update("acct-a", &accountScoped)
	if got := tracker.Compute("acct-b", &accountScoped); got.CacheReadInputTokens != 0 {
		t.Fatalf("account scope must not cross accounts, got %+v", got)
	}
}

func TestPromptCacheModes(t *testing.T) {
	useCompat(t, config.CompatSettings{PromptCacheMode: config.PromptCacheModeReadOnly})
	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0)
	if got := tracker.Compute("acct", profile); got.CacheCreationInputTokens != 0 || got.CacheCreation5mInputTokens != 0 {
		t.Fatalf("read_only must never report creation, got %+v", got)
	}
	tracker.Update("acct", profile)
	if got := tracker.Compute("acct", profile); got.CacheReadInputTokens == 0 {
		t.Fatalf("read_only must still report reads, got %+v", got)
	}

	_ = config.UpdateCompatSettings(config.CompatSettings{PromptCacheMode: config.PromptCacheModeOff})
	if p := tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0); p != nil {
		t.Fatalf("off mode must not build a profile")
	}
}

func TestPromptCacheImageDoesNotInflateCounts(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	img := map[string]interface{}{"type": "image", "source": map[string]interface{}{
		"type": "base64", "media_type": "image/png",
		"data": base64.StdEncoding.EncodeToString(noisyPNG(t, 800, 600)),
	}}
	req := cachedSystemRequest("claude-sonnet-4.5",
		ClaudeMessage{Role: "assistant", Content: "ok"},
		ClaudeMessage{Role: "user", Content: []interface{}{img, map[string]interface{}{"type": "text", "text": "describe"}}},
	)
	est := estimateClaudeRequestInputTokens(req)
	if est > 20000 {
		t.Fatalf("an 800x600 image must cost about 640 tokens, request estimate is %d", est)
	}
	if got := estimateImageBlockTokens(img); got != 640 {
		t.Fatalf("image tokens = %d, want 640", got)
	}

	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(req, est)
	usage := tracker.Compute("acct", profile)
	m := buildClaudeUsageMap(3000, 10, usage, true)
	sum := m["input_tokens"].(int) + m["cache_creation_input_tokens"].(int) + m["cache_read_input_tokens"].(int)
	if sum != 3000 {
		t.Fatalf("usage must add up to the real total, got %v", m)
	}
}

func TestPromptCacheIgnoresToolIDDrift(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	withIDs := func(useID, resultID string) *ClaudeRequest {
		return cachedSystemRequest("claude-sonnet-4.5",
			ClaudeMessage{Role: "assistant", Content: []interface{}{map[string]interface{}{"type": "tool_use", "id": useID, "name": "Read", "input": map[string]interface{}{"path": "a"}}}},
			ClaudeMessage{Role: "user", Content: []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": resultID, "content": "body"}}},
		)
	}
	tracker := newPromptCacheTracker(time.Hour)
	first := tracker.BuildClaudeProfile(withIDs("toolu_1", "toolu_1"), 0)
	tracker.Update("acct", first)
	second := tracker.BuildClaudeProfile(withIDs("toolu_regenerated", "toolu_regenerated"), 0)
	if got := tracker.Compute("acct", second); got.CacheCreationInputTokens != 0 {
		t.Fatalf("regenerated tool IDs must still hit the whole prefix, got %+v", got)
	}
}

func TestPromptCachePersistsAcrossRestart(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	path := filepath.Join(t.TempDir(), "prompt_cache.json")
	tracker := newPromptCacheTracker(time.Hour)
	profile := tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0)
	profile.Scope = "sess:persist"
	tracker.Update("acct", profile)
	if err := tracker.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	restarted := newPromptCacheTracker(time.Hour)
	if err := restarted.load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := restarted.Compute("acct", profile); got.CacheReadInputTokens == 0 {
		t.Fatalf("persisted prefix must hit after restart, got %+v", got)
	}
}

func TestPromptCacheEvictsLeastRecentlyUsed(t *testing.T) {
	useCompat(t, config.CompatSettings{PromptCacheMaxEntries: 10})
	tracker := newPromptCacheTracker(time.Hour)
	for i := 0; i < 30; i++ {
		profile := tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0)
		profile.Scope = "sess:" + string(rune('a'+i))
		tracker.Update("acct", profile)
	}
	total := 0
	for _, entries := range tracker.entriesByScope {
		total += len(entries)
	}
	if total > 10 {
		t.Fatalf("cache holds %d entries, cap is 10", total)
	}
}

func TestClaudeSessionCacheHitsAfterAccountRotation(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		writeKiroSuccess(t, w, "ok")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("cache-rotate")...)

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4.5",
		"max_tokens": 100,
		"metadata":   map[string]interface{}{"user_id": `{"session_id":"rotate-session"}`},
		"system": []interface{}{map[string]interface{}{
			"type": "text", "text": strings.Repeat("Stable project rules for the assistant. ", 200),
			"cache_control": map[string]interface{}{"type": "ephemeral"},
		}},
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	})
	send := func() ClaudeResponse {
		rec := httptest.NewRecorder()
		h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
		var resp ClaudeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return resp
	}

	first := send()
	second := send() // round robin moves this to the other account
	if calls := srv.calls(); len(calls) != 2 || calls[0] == calls[1] {
		t.Fatalf("expected the two requests on different accounts, got %v", calls)
	}
	if first.Usage.CacheCreationInputTokens == 0 || first.Usage.CacheReadInputTokens != 0 {
		t.Fatalf("first request must create, got %+v", first.Usage)
	}
	if second.Usage.CacheReadInputTokens == 0 || second.Usage.CacheCreationInputTokens != 0 {
		t.Fatalf("same session must hit after rotating accounts, got %+v", second.Usage)
	}
}

func TestAnthropicCacheSwitchOmitsUsage(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		writeKiroSuccess(t, w, "ok")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("cache-off")...)
	off := false
	if err := config.UpdateCompatSettings(config.CompatSettings{SimulateAnthropicCache: &off}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })

	body, _ := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4.5",
		"max_tokens": 100,
		"metadata":   map[string]interface{}{"user_id": `{"session_id":"off-session"}`},
		"system": []interface{}{map[string]interface{}{
			"type": "text", "text": strings.Repeat("Stable project rules for the assistant. ", 200),
			"cache_control": map[string]interface{}{"type": "ephemeral"},
		}},
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "hi"}},
	})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
		var resp ClaudeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		if resp.Usage.CacheCreationInputTokens != 0 || resp.Usage.CacheReadInputTokens != 0 {
			t.Fatalf("request %d leaked cache usage %+v", i, resp.Usage)
		}
	}
}

func openAICacheRequest(callID, userText string) *OpenAIRequest {
	call := ToolCall{ID: callID, Type: "function"}
	call.Function.Name = "read"
	call.Function.Arguments = "{\"path\":\"README.md\"}"
	return &OpenAIRequest{
		Model: "claude-sonnet-4.5",
		Messages: []OpenAIMessage{
			{Role: "system", Content: strings.Repeat("Stable project rules for the assistant. ", 200)},
			{Role: "user", Content: "inspect the repository"},
			{Role: "assistant", ToolCalls: []ToolCall{call}},
			{Role: "tool", ToolCallID: callID, Content: "file contents"},
			{Role: "user", Content: userText},
		},
	}
}

func TestOpenAIProfileHitsWhenToolCallIDChanges(t *testing.T) {
	on := true
	useCompat(t, config.CompatSettings{SimulateOpenAICache: &on})
	tracker := newPromptCacheTracker(time.Hour)
	if tracker.BuildOpenAIProfile(openAICacheRequest("call-a", "summarize"), 0) == nil {
		t.Fatal("switch on must build a profile")
	}
	first := tracker.BuildOpenAIProfile(openAICacheRequest("call-a", "summarize"), 0)
	first.Scope = "key:one"
	if tracker.Compute("acct", first).CacheReadInputTokens != 0 {
		t.Fatal("first sighting is not a read")
	}
	tracker.Update("acct", first)
	second := tracker.BuildOpenAIProfile(openAICacheRequest("call-b", "summarize"), 0)
	second.Scope = "key:one"
	got := tracker.Compute("acct", second)
	cached := openAICachedTokens(got.PromptTotalTokens, got)
	if cached <= 0 || cached > got.PromptTotalTokens {
		t.Fatalf("cached=%d total=%d usage=%+v", cached, got.PromptTotalTokens, got)
	}
	tail := tracker.BuildOpenAIProfile(openAICacheRequest("call-b", "hi"), 0)
	tail.Scope = "key:one"
	tailUsage := tracker.Compute("acct", tail)
	if openAICachedTokens(tailUsage.PromptTotalTokens, tailUsage) <= 0 {
		t.Fatal("a new tail must still hit the earlier prefix")
	}
}

func TestOpenAICacheSwitchDefaultsOff(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	if tracker.BuildOpenAIProfile(openAICacheRequest("call-a", "summarize"), 0) != nil {
		t.Fatal("OpenAI simulation must stay off until an admin enables it")
	}
	if tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0) == nil {
		t.Fatal("Anthropic simulation stays on by default")
	}
	off := false
	if err := config.UpdateCompatSettings(config.CompatSettings{SimulateAnthropicCache: &off}); err != nil {
		t.Fatal(err)
	}
	if tracker.BuildClaudeProfile(cachedSystemRequest("claude-sonnet-4.5"), 0) != nil {
		t.Fatal("Anthropic switch off must skip the profile")
	}
}

func TestOpenAIChatUsageReportsCachedTokens(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		writeKiroSuccess(t, w, "ok")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("openai-cache")[0])
	on := true
	if err := config.UpdateCompatSettings(config.CompatSettings{SimulateOpenAICache: &on}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })

	send := func(callID, text string) (prompt, cached float64, hasDetails bool) {
		raw, _ := json.Marshal(openAICacheRequest(callID, text))
		rec := httptest.NewRecorder()
		h.handleOpenAIChat(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(raw))))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Usage struct {
				PromptTokens int `json:"prompt_tokens"`
				Details      *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Usage.Details != nil {
			return float64(resp.Usage.PromptTokens), float64(resp.Usage.Details.CachedTokens), true
		}
		return float64(resp.Usage.PromptTokens), 0, false
	}

	prompt, cached, details := send("call-a", "summarize")
	if details || cached != 0 || prompt <= 0 {
		t.Fatalf("first usage prompt=%v cached=%v details=%v", prompt, cached, details)
	}
	prompt, cached, details = send("call-b", "summarize")
	if !details || cached <= 0 || cached > prompt {
		t.Fatalf("second usage prompt=%v cached=%v details=%v", prompt, cached, details)
	}
	_, cached, details = send("call-c", "hi")
	if cached <= 0 {
		t.Fatal("changed tail should still read the stable prefix")
	}
	short := `{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.handleOpenAIChat(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(short)))
		var resp struct {
			Usage struct {
				Details *struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Usage.Details != nil && resp.Usage.Details.CachedTokens > 0 {
			t.Fatalf("short request %d reported cache %+v", i, resp.Usage.Details)
		}
	}
}
