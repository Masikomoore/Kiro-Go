package proxy

import (
	"encoding/json"
	"fmt"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func kiroConversation(turns int) *KiroPayload {
	msgs := []ClaudeMessage{}
	for i := 0; i < turns; i++ {
		msgs = append(msgs, ClaudeMessage{Role: "user", Content: fmt.Sprintf("question %d: %s", i, strings.Repeat("explain the module. ", 120))})
		if i < turns-1 {
			msgs = append(msgs, ClaudeMessage{Role: "assistant", Content: fmt.Sprintf("answer %d: %s", i, strings.Repeat("it handles requests. ", 120))})
		}
	}
	return ClaudeToKiro(&ClaudeRequest{Model: "claude-sonnet-4.5", System: strings.Repeat("You are a helpful assistant. ", 100), Messages: msgs}, false)
}

func TestKiroProfileHitsGrowingConversation(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	first := tracker.BuildKiroProfile(kiroConversation(3), "claude-sonnet-4.5", false)
	if first == nil || !first.Kiro {
		t.Fatalf("expected a Kiro profile, got %+v", first)
	}
	tracker.Update("acct-1", first)
	second := tracker.BuildKiroProfile(kiroConversation(4), "claude-sonnet-4.5", false)
	got := tracker.Compute("acct-1", second)
	if got.CacheReadInputTokens < first.Breakpoints[len(first.Breakpoints)-1].CumulativeTokens {
		t.Fatalf("the previous turn's history must be read back, got %+v", got)
	}
}

// When kiro-go changes an early history entry (a moved truncation boundary,
// say), upstream cannot reuse the prefix past it, and neither may the report.
func TestKiroProfileStopsAtChangedEntry(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	first := kiroConversation(4)
	tracker.Update("acct-1", tracker.BuildKiroProfile(first, "claude-sonnet-4.5", false))

	second := kiroConversation(5)
	second.ConversationState.History[2].UserInputMessage.Content = "different first kept turn"
	profile := tracker.BuildKiroProfile(second, "claude-sonnet-4.5", false)
	got := tracker.Compute("acct-1", profile)
	limit := profile.Breakpoints[2].CumulativeTokens // prelude + entries 0..1
	if got.CacheReadInputTokens > limit {
		t.Fatalf("read %d tokens past the changed entry (limit %d)", got.CacheReadInputTokens, limit)
	}
	if got.CacheCreationInputTokens == 0 {
		t.Fatalf("the changed tail must be reported as creation, got %+v", got)
	}
}

// A WorkBuddy-style client (no session, no API key, no cache_control) stays
// on one account through the conversation fallback key, and its next turn is
// reported as a cache read of the history Kiro already saw.
func TestNoSessionConversationStaysOnAccountAndHits(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		writeKiroSuccess(t, w, "ok")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("conv-sticky")...)
	h.affinity = newAccountAffinity()

	turn := func(n int) ClaudeResponse {
		msgs := []interface{}{}
		for i := 0; i < n; i++ {
			msgs = append(msgs, map[string]interface{}{"role": "user", "content": fmt.Sprintf("question %d: %s", i, strings.Repeat("explain the module. ", 120))})
			if i < n-1 {
				msgs = append(msgs, map[string]interface{}{"role": "assistant", "content": fmt.Sprintf("answer %d: %s", i, strings.Repeat("it handles requests. ", 120))})
			}
		}
		body, _ := json.Marshal(map[string]interface{}{
			"model": "claude-sonnet-4.5", "max_tokens": 100,
			"system":   strings.Repeat("You are WorkBuddy, a desktop assistant. ", 150),
			"messages": msgs,
		})
		rec := httptest.NewRecorder()
		h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
		var resp ClaudeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return resp
	}

	first := turn(2)
	second := turn(3)
	if calls := srv.calls(); len(calls) != 2 || calls[0] != calls[1] {
		t.Fatalf("expected both turns on one account, got %v", calls)
	}
	if first.Usage.CacheReadInputTokens != 0 || first.Usage.CacheCreationInputTokens == 0 {
		t.Fatalf("first turn must create, got %+v", first.Usage)
	}
	if second.Usage.CacheReadInputTokens == 0 {
		t.Fatalf("second turn must read the first turn's history, got %+v", second.Usage)
	}
}

// With cache_control on the newest message the current turn is a cache write,
// so almost nothing is left as plain input.
func TestKiroProfileIncludesCurrentForExplicitClients(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	payload := kiroConversation(3)
	without := tracker.BuildKiroProfile(payload, "claude-sonnet-4.5", false)
	with := tracker.BuildKiroProfile(payload, "claude-sonnet-4.5", true)
	if len(with.Breakpoints) != len(without.Breakpoints)+1 {
		t.Fatalf("expected one extra boundary for the current message: %d vs %d", len(with.Breakpoints), len(without.Breakpoints))
	}
	last := with.Breakpoints[len(with.Breakpoints)-1].CumulativeTokens
	if last <= without.Breakpoints[len(without.Breakpoints)-1].CumulativeTokens {
		t.Fatal("the current message must add tokens to the cached prefix")
	}
}

// The split is computed against the Kiro payload, so a cache_control client
// with an unchanged history reports almost no plain input, whatever the client
// estimate counted.
func TestKiroProfileSplitUsesKiroBasis(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	p := tracker.BuildKiroProfile(kiroConversation(3), "claude-sonnet-4.5", true)
	tracker.Update("acct-1", p)
	usage := tracker.Compute("acct-1", tracker.BuildKiroProfile(kiroConversation(3), "claude-sonnet-4.5", true))
	split := usage.split(200000)
	if split.Input > 200000/100 {
		t.Fatalf("an unchanged cache_control turn must be nearly all cache, got %+v", split)
	}
}

// Tool specs are sent once, in the prelude; the current message must not count
// them again, or every turn reports the whole tool list as a new cache write.
func TestKiroProfileCountsToolsOnce(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	build := func(withTools bool) *KiroPayload {
		req := &ClaudeRequest{Model: "claude-sonnet-4.5", System: strings.Repeat("You are a helpful assistant. ", 100), Messages: []ClaudeMessage{
			{Role: "user", Content: "question 0: " + strings.Repeat("explain the module. ", 120)},
			{Role: "assistant", Content: "answer 0: " + strings.Repeat("it handles requests. ", 120)},
			{Role: "user", Content: "question 1"},
		}}
		if withTools {
			for i := 0; i < 20; i++ {
				req.Tools = append(req.Tools, ClaudeTool{Name: fmt.Sprintf("tool_%d", i), Description: strings.Repeat("does a thing. ", 60),
					InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}}}})
			}
		}
		return ClaudeToKiro(req, false)
	}
	withTools := build(true)
	toolTokens := estimateJSONTokens(withTools.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools)
	last := func(p *promptCacheProfile) int { return p.Breakpoints[len(p.Breakpoints)-1].CumulativeTokens }
	diff := last(tracker.BuildKiroProfile(withTools, "claude-sonnet-4.5", true)) - last(tracker.BuildKiroProfile(build(false), "claude-sonnet-4.5", true))
	if diff != toolTokens {
		t.Fatalf("tools must be counted once (%d tokens), the profile grew by %d", toolTokens, diff)
	}
	if withTools.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools == nil {
		t.Fatal("BuildKiroProfile must not modify the payload")
	}
}

// A cache_control client's newest message is written as the last boundary.
// With tools on the request, stripping them left an empty context {} that the
// same message lacks once it moves into history, so the next turn never read
// it back.
func TestKiroProfileCurrentMessageMatchesItInHistory(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	tracker := newPromptCacheTracker(time.Hour)
	withTools := func(turns int) *KiroPayload {
		p := kiroConversation(turns)
		p.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext = &UserInputMessageContext{Tools: []KiroToolWrapper{{}}}
		return p
	}
	first := tracker.BuildKiroProfile(withTools(3), "claude-sonnet-4.5", true)
	tracker.Update("acct-1", first)
	got := tracker.Compute("acct-1", tracker.BuildKiroProfile(withTools(4), "claude-sonnet-4.5", true))
	if want := first.Breakpoints[len(first.Breakpoints)-1].CumulativeTokens; got.CacheReadInputTokens < want {
		t.Fatalf("next turn read %d, want the whole previous prompt (%d) including its newest message", got.CacheReadInputTokens, want)
	}
}

// Without cache_control the stream reports usage only in message_delta, so a
// stream that fails after message_start is not billed downstream. Requests
// with cache_control (Claude Code) keep their usage in message_start.
func TestStreamUsageWaitsForMessageDeltaWithoutCacheControl(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeKiroSuccess(t, w, "ok")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("defer-usage")...)

	usage := func(cacheControl bool) (start, delta map[string]interface{}) {
		last := map[string]interface{}{"type": "text", "text": "question 1"}
		if cacheControl {
			last["cache_control"] = map[string]interface{}{"type": "ephemeral"}
		}
		body, _ := json.Marshal(map[string]interface{}{
			"model": "claude-sonnet-4.5", "max_tokens": 100, "stream": true,
			"system": strings.Repeat("You are WorkBuddy, a desktop assistant. ", 150),
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": "question 0: " + strings.Repeat("explain the module. ", 120)},
				map[string]interface{}{"role": "assistant", "content": "answer 0: " + strings.Repeat("it handles requests. ", 120)},
				map[string]interface{}{"role": "user", "content": []interface{}{last}},
			},
		})
		rec := httptest.NewRecorder()
		h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			var ev struct {
				Type    string                 `json:"type"`
				Usage   map[string]interface{} `json:"usage"`
				Message struct {
					Usage map[string]interface{} `json:"usage"`
				} `json:"message"`
			}
			if !strings.HasPrefix(line, "data: ") || json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
				continue
			}
			switch ev.Type {
			case "message_start":
				start = ev.Message.Usage
			case "message_delta":
				delta = ev.Usage
			}
		}
		return start, delta
	}
	billable := func(u map[string]interface{}) float64 {
		n, _ := u["input_tokens"].(float64)
		c, _ := u["cache_creation_input_tokens"].(float64)
		r, _ := u["cache_read_input_tokens"].(float64)
		return n + c + r
	}

	start, delta := usage(false)
	if billable(start) != 0 {
		t.Fatalf("message_start must carry no billable usage without cache_control, got %v", start)
	}
	if billable(delta) == 0 {
		t.Fatalf("message_delta must carry the full usage, got %v", delta)
	}
	start, delta = usage(true)
	if billable(start) == 0 || billable(start) != billable(delta) {
		t.Fatalf("cache_control clients keep usage in message_start: start %v delta %v", start, delta)
	}
}

// A client retry that overlaps the first request must read the prefix as soon
// as the first response has begun, as Kiro's own cache does (live 2026-10-19:
// the retry cost 1.04 credits against 2.38 but was reported as a full write).
func TestOverlappingRequestReadsPrefixOnceFirstResponseBegins(t *testing.T) {
	first := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n > 1 {
			writeKiroSuccess(t, w, "ok")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "partial"}))
		w.(http.Flusher).Flush()
		close(first)
		<-release
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("overlap")[:1]...)

	body, _ := json.Marshal(map[string]interface{}{
		"model": "claude-sonnet-4.5", "max_tokens": 100, "stream": true,
		"system": strings.Repeat("You are WorkBuddy, a desktop assistant. ", 150),
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "question 0: " + strings.Repeat("explain the module. ", 120)},
			map[string]interface{}{"role": "assistant", "content": "answer 0: " + strings.Repeat("it handles requests. ", 120)},
			map[string]interface{}{"role": "user", "content": "question 1"},
		},
	})
	send := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
		return rec
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		send()
	}()
	<-first
	// The handler reads the flushed first event shortly after the server writes it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.promptCache.mu.Lock()
		n := 0
		for _, entries := range h.promptCache.entriesByScope {
			n += len(entries)
		}
		h.promptCache.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			<-done
			t.Fatal("the first request's prefix was not cached while its response was streaming")
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := send()
	close(release)
	<-done
	var read float64
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		var ev struct {
			Type  string                 `json:"type"`
			Usage map[string]interface{} `json:"usage"`
		}
		if strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) == nil && ev.Type == "message_delta" {
			read, _ = ev.Usage["cache_read_input_tokens"].(float64)
		}
	}
	if read == 0 {
		t.Fatalf("overlapping retry must read the cached prefix, got body %s", rec.Body.String())
	}
}
