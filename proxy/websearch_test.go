package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"kiro-go/config"
)

func TestHasWebSearchTool_SingleNativeTool(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4",
		Tools: []ClaudeTool{
			{Type: "web_search_20250305", Name: "web_search"},
		},
	}
	if !hasWebSearchTool(req) {
		t.Fatal("expected single native web_search tool to be detected")
	}
	if hasWebSearchAmongTools(req) {
		t.Fatal("single native tool must not be treated as mixed")
	}
}

func TestHasWebSearchTool_MultipleTools(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4",
		Tools: []ClaudeTool{
			{Type: "web_search_20250305", Name: "web_search"},
			{Name: "other_tool", Description: "Other"},
		},
	}
	if hasWebSearchTool(req) {
		t.Fatal("multiple tools must not be treated as pure web_search request")
	}
	if !hasWebSearchAmongTools(req) {
		t.Fatal("expected mixed tools with native web_search")
	}
}

func TestHasWebSearchTool_RegularToolNamedWebSearch(t *testing.T) {
	// Client-defined tools named web_search without type web_search_* must not
	// take the native path.
	req := &ClaudeRequest{
		Model: "claude-sonnet-4",
		Tools: []ClaudeTool{
			{Name: "web_search", Description: "Regular client-side search tool"},
		},
	}
	if hasWebSearchTool(req) {
		t.Fatal("regular tool named web_search must not trigger native websearch")
	}
	if isNativeWebSearchTool(req.Tools[0]) {
		t.Fatal("regular tool must not be native")
	}
}

func TestHasWebSearchTool_NoTools(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-sonnet-4"}
	if hasWebSearchTool(req) {
		t.Fatal("request without tools must not be treated as web_search request")
	}
	if hasWebSearchAmongTools(req) {
		t.Fatal("request without tools must not be mixed web_search")
	}
}

func TestExtractSearchQuery_StringContentWithPrefix(t *testing.T) {
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Perform a web search for the query: rust latest version 2026"},
		},
	}
	got := extractSearchQuery(req)
	want := "rust latest version 2026"
	if got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

func TestExtractSearchQuery_StringContentWithoutPrefix(t *testing.T) {
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: "typescript 5.7 features"},
		},
	}
	if got := extractSearchQuery(req); got != "typescript 5.7 features" {
		t.Fatalf("query = %q", got)
	}
}

func TestExtractSearchQuery_LastUserTurn(t *testing.T) {
	req := &ClaudeRequest{
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Perform a web search for the query: first"},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "Perform a web search for the query: second"},
		},
	}
	if got := extractSearchQuery(req); got != "second" {
		t.Fatalf("query = %q, want second", got)
	}
}

func TestExtractSearchQuery_ArrayContent(t *testing.T) {
	// JSON decode yields []interface{} for content arrays.
	raw := `{
		"model": "claude-sonnet-4",
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Perform a web search for the query: golang generics"}
			]}
		]
	}`
	var req ClaudeRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := extractSearchQuery(&req); got != "golang generics" {
		t.Fatalf("query = %q", got)
	}
}

func TestExtractSearchQuery_Empty(t *testing.T) {
	req := &ClaudeRequest{}
	if got := extractSearchQuery(req); got != "" {
		t.Fatalf("expected empty query, got %q", got)
	}
}

func TestCreateMcpRequest_Format(t *testing.T) {
	toolUseID, mcpReq := createMcpRequest("hello")

	if !strings.HasPrefix(toolUseID, "srvtoolu_") {
		t.Errorf("toolUseID prefix wrong: %q", toolUseID)
	}
	if len(toolUseID) != len("srvtoolu_")+32 {
		t.Errorf("toolUseID length = %d", len(toolUseID))
	}
	if !strings.HasPrefix(mcpReq.ID, "web_search_tooluse_") {
		t.Errorf("mcp request ID prefix wrong: %q", mcpReq.ID)
	}
	if mcpReq.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q", mcpReq.JSONRPC)
	}
	if mcpReq.Method != "tools/call" {
		t.Errorf("method = %q", mcpReq.Method)
	}
	if mcpReq.Params.Name != "web_search" {
		t.Errorf("params.name = %q", mcpReq.Params.Name)
	}
	if mcpReq.Params.Arguments.Query != "hello" {
		t.Errorf("query = %q", mcpReq.Params.Arguments.Query)
	}
}

func TestParseSearchResults(t *testing.T) {
	inner := `{"results":[{"title":"T1","url":"https://e.com","snippet":"s1","publishedDate":1732299319000,"id":"1","domain":"e.com"}],"totalResults":1,"query":"q"}`
	mcpResp := &McpResponse{
		Result: &McpResult{
			Content: []McpContent{{Type: "text", Text: inner}},
		},
	}
	results := parseSearchResults(mcpResp)
	if results == nil {
		t.Fatal("expected results")
	}
	if len(results.Results) != 1 {
		t.Fatalf("results len = %d", len(results.Results))
	}
	r := results.Results[0]
	if r.Title != "T1" || r.URL != "https://e.com" {
		t.Errorf("unexpected result: %+v", r)
	}
	if r.Snippet == nil || *r.Snippet != "s1" {
		t.Errorf("snippet mismatch: %+v", r.Snippet)
	}
}

func TestParseSearchResults_EmbeddedError(t *testing.T) {
	inner := `{"results":[],"error":"upstream failed"}`
	mcpResp := &McpResponse{
		Result: &McpResult{
			Content: []McpContent{{Type: "text", Text: inner}},
		},
	}
	if parseSearchResults(mcpResp) != nil {
		t.Fatal("embedded error must yield nil")
	}
}

func TestParseSearchResults_IsErrorFlag(t *testing.T) {
	mcpResp := &McpResponse{
		Result: &McpResult{
			IsError: true,
			Content: []McpContent{{Type: "text", Text: `{"results":[]}`}},
		},
	}
	if parseSearchResults(mcpResp) != nil {
		t.Fatal("isError must yield nil")
	}
}

func TestParseSearchResults_NonTextContent(t *testing.T) {
	mcpResp := &McpResponse{
		Result: &McpResult{Content: []McpContent{{Type: "image", Text: ""}}},
	}
	if parseSearchResults(mcpResp) != nil {
		t.Fatal("non-text content must yield nil")
	}
}

func TestParseSearchResults_NilResult(t *testing.T) {
	if parseSearchResults(&McpResponse{}) != nil {
		t.Fatal("nil result must yield nil")
	}
}

func TestGenerateSearchSummary_WithResults(t *testing.T) {
	s1 := "a snippet"
	results := &WebSearchResults{
		Results: []WebSearchResult{
			{Title: "Title One", URL: "https://one.com", Snippet: &s1},
		},
	}
	summary := generateSearchSummary("myquery", results)
	for _, want := range []string{"myquery", "Title One", "https://one.com", "a snippet"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestGenerateSearchSummary_NoResults(t *testing.T) {
	summary := generateSearchSummary("q", nil)
	if !strings.Contains(summary, "No results found") {
		t.Errorf("expected 'No results found':\n%s", summary)
	}
}

func TestGenerateSearchSummary_LongSnippetTruncated(t *testing.T) {
	long := strings.Repeat("x", 300)
	results := &WebSearchResults{
		Results: []WebSearchResult{{Title: "T", URL: "https://e.com", Snippet: &long}},
	}
	summary := generateSearchSummary("q", results)
	if !strings.Contains(summary, "...") {
		t.Error("expected long snippet to be truncated with ellipsis")
	}
}

func TestChunkByRunes(t *testing.T) {
	chunks := chunkByRunes("abcdef", 2)
	if len(chunks) != 3 {
		t.Fatalf("chunks = %v", chunks)
	}
	if strings.Join(chunks, "") != "abcdef" {
		t.Errorf("rejoined = %q", strings.Join(chunks, ""))
	}
}

func TestChunkByRunes_Multibyte(t *testing.T) {
	in := "あいうえおかきくけこ"
	chunks := chunkByRunes(in, 3)
	if strings.Join(chunks, "") != in {
		t.Errorf("multibyte rejoin failed: %q", strings.Join(chunks, ""))
	}
	for _, c := range chunks {
		if !json.Valid([]byte(`"` + c + `"`)) {
			t.Errorf("chunk not valid utf-8: %q", c)
		}
	}
}

func TestShouldSearchRound(t *testing.T) {
	web := []KiroToolUse{{Name: "web_search"}, {Name: "web_search"}}
	if !shouldSearchRound(0, web, maxWebSearchRounds) {
		t.Fatal("pure web_search should continue")
	}
	if shouldSearchRound(maxWebSearchRounds, web, maxWebSearchRounds) {
		t.Fatal("at limit should stop")
	}
	if shouldSearchRound(1, web, 1) {
		t.Fatal("max_uses=1 must stop after first search round index")
	}
	mixed := []KiroToolUse{{Name: "web_search"}, {Name: "Bash"}}
	if shouldSearchRound(0, mixed, maxWebSearchRounds) {
		t.Fatal("mixed tools should not continue loop")
	}
	if shouldSearchRound(0, nil, maxWebSearchRounds) {
		t.Fatal("empty tool uses should not continue")
	}
}

func TestResolveWebSearchMaxUses(t *testing.T) {
	if got := resolveWebSearchMaxUses(nil); got != maxWebSearchRounds {
		t.Fatalf("nil tools: got %d", got)
	}
	if got := resolveWebSearchMaxUses([]ClaudeTool{
		{Type: "web_search_20250305", Name: "web_search", MaxUses: 2},
		{Name: "Bash"},
	}); got != 2 {
		t.Fatalf("max_uses=2: got %d", got)
	}
	if got := resolveWebSearchMaxUses([]ClaudeTool{
		{Type: "web_search_20250305", Name: "web_search", MaxUses: 99},
	}); got != maxWebSearchRounds {
		t.Fatalf("max_uses must be capped at %d, got %d", maxWebSearchRounds, got)
	}
	if got := resolveWebSearchMaxUses([]ClaudeTool{
		{Type: "web_search_20250305", Name: "web_search"},
	}); got != maxWebSearchRounds {
		t.Fatalf("omitted max_uses: got %d", got)
	}
}

func TestResolveFlushStopReason(t *testing.T) {
	if got := resolveFlushStopReason("max_tokens", nil, nil); got != "max_tokens" {
		t.Fatalf("override: %q", got)
	}
	client := []KiroToolUse{{Name: "Bash"}}
	if got := resolveFlushStopReason("", client, nil); got != "tool_use" {
		t.Fatalf("client tool: %q", got)
	}
	webOnly := []KiroToolUse{{Name: "web_search"}}
	if got := resolveFlushStopReason("", webOnly, nil); got != "end_turn" {
		t.Fatalf("web only: %q", got)
	}
	contentWithClient := []map[string]interface{}{
		{"type": "tool_use", "name": "Bash"},
	}
	if got := resolveFlushStopReason("", nil, contentWithClient); got != "tool_use" {
		t.Fatalf("content client tool: %q", got)
	}
}

func TestBuildFlushContent_WebSearchAsServerTool(t *testing.T) {
	snippet := "hello"
	results := &WebSearchResults{
		Results: []WebSearchResult{{Title: "T", URL: "https://e.com", Snippet: &snippet}},
	}
	content := buildFlushContent(nil, "done", []KiroToolUse{
		{ToolUseID: "toolu_1", Name: "web_search", Input: map[string]interface{}{"query": "q"}},
		{ToolUseID: "toolu_2", Name: "Bash", Input: map[string]interface{}{"command": "ls"}},
	}, []*WebSearchResults{results, nil})

	var sawServer, sawClient, sawRawWeb bool
	for _, b := range content {
		switch b["type"] {
		case "server_tool_use":
			sawServer = true
			if b["name"] != "web_search" {
				t.Errorf("server tool name = %v", b["name"])
			}
		case "web_search_tool_result":
			// ok
		case "tool_use":
			if b["name"] == "web_search" {
				sawRawWeb = true
			}
			if b["name"] == "Bash" {
				sawClient = true
			}
		}
	}
	if !sawServer {
		t.Error("expected server_tool_use for web_search")
	}
	if !sawClient {
		t.Error("expected raw tool_use for Bash")
	}
	if sawRawWeb {
		t.Error("web_search must not be flushed as raw tool_use")
	}
}

func TestConvertClaudeTools_SkipsNativeWebSearchAndInjectsSchema(t *testing.T) {
	tools := []ClaudeTool{
		{Type: "web_search_20250305", Name: "web_search"},
		{Name: "Bash", Description: "run", InputSchema: map[string]interface{}{"type": "object"}},
	}
	kiroTools, nameMap := convertClaudeTools(tools)
	var sawWeb, sawClient bool
	for _, tspec := range kiroTools {
		name := tspec.ToolSpecification.Name
		switch {
		case name == "web_search":
			sawWeb = true
			schema, ok := tspec.ToolSpecification.InputSchema.JSON.(map[string]interface{})
			if !ok {
				t.Fatal("web_search schema must be object map")
			}
			props, _ := schema["properties"].(map[string]interface{})
			if props == nil || props["query"] == nil {
				t.Fatalf("web_search schema missing query: %+v", schema)
			}
		case name == "Bash" || name == "bash" || nameMap[name] == "Bash":
			sawClient = true
		}
	}
	if !sawWeb {
		t.Error("expected injected web_search schema for mixed tools")
	}
	if !sawClient {
		t.Errorf("expected client Bash tool in %+v (nameMap=%v)", kiroTools, nameMap)
	}
}

func TestConvertClaudeTools_PureNativeWebSearchSkipped(t *testing.T) {
	// Pure path never calls convertClaudeTools. If it did, native-only must not
	// inject a Kiro web_search (no client tools remain after filtering).
	tools := []ClaudeTool{
		{Type: "web_search_20250305", Name: "web_search"},
	}
	kiroTools, _ := convertClaudeTools(tools)
	if len(kiroTools) != 0 {
		t.Fatalf("pure native web_search must not produce Kiro tools, got %+v", kiroTools)
	}
}

func TestClaudeToolTypeJSONRoundTrip(t *testing.T) {
	raw := `{"type":"web_search_20250305","name":"web_search","max_uses":8}`
	var tool ClaudeTool
	if err := json.Unmarshal([]byte(raw), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Type != "web_search_20250305" || tool.Name != "web_search" || tool.MaxUses != 8 {
		t.Fatalf("tool = %+v", tool)
	}
	if !isNativeWebSearchTool(tool) {
		t.Fatal("expected native")
	}
}

func TestBuildWebSearchContentBlocks(t *testing.T) {
	s := "snip"
	results := &WebSearchResults{
		Results: []WebSearchResult{{Title: "T", URL: "https://e.com", Snippet: &s}},
	}
	blocks := buildWebSearchContentBlocks("q", "srvtoolu_abc", results)
	if len(blocks) != 4 {
		t.Fatalf("blocks len = %d", len(blocks))
	}
	if blocks[1]["type"] != "server_tool_use" {
		t.Errorf("block1 type = %v", blocks[1]["type"])
	}
	if blocks[2]["type"] != "web_search_tool_result" {
		t.Errorf("block2 type = %v", blocks[2]["type"])
	}
}

// Regression: appendSearchRound must store content in a shape ClaudeToKiro can
// extract (tool_use + tool_result pairing). Previously []map was dropped.
func TestAppendSearchRound_ContentSurvivesClaudeToKiro(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-sonnet-4",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "search something"},
		},
		Tools: []ClaudeTool{
			{Type: "web_search_20250305", Name: "web_search", MaxUses: 3},
			{Name: "Bash", Description: "run", InputSchema: map[string]interface{}{"type": "object"}},
		},
	}
	round := &webSearchRoundOutcome{
		text: "Looking it up.",
		toolUses: []KiroToolUse{
			{
				ToolUseID: "toolu_ws_1",
				Name:      "web_search",
				Input:     map[string]interface{}{"query": "golang generics"},
			},
		},
	}
	snippet := "Go 1.18 introduced generics"
	searched := []*WebSearchResults{
		{Results: []WebSearchResult{{Title: "Generics", URL: "https://go.dev", Snippet: &snippet}}},
	}
	presentation := make([]map[string]interface{}, 0)
	appendSearchRound(req, round, searched, &presentation)

	if len(req.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3 (user + assistant + tool_result user)", len(req.Messages))
	}
	if len(presentation) != 2 {
		t.Fatalf("presentation len = %d, want server_tool_use + web_search_tool_result", len(presentation))
	}

	// Direct extract on the in-memory shapes.
	aText, aUses := extractClaudeAssistantContent(req.Messages[1].Content)
	if aText != "Looking it up." || len(aUses) != 1 || aUses[0].ToolUseID != "toolu_ws_1" {
		t.Fatalf("assistant extract failed: text=%q uses=%+v", aText, aUses)
	}
	_, _, uResults := extractClaudeUserContent(req.Messages[2].Content)
	if len(uResults) != 1 || uResults[0].ToolUseID != "toolu_ws_1" {
		t.Fatalf("user tool_result extract failed: %+v", uResults)
	}
	if len(uResults[0].Content) == 0 || !strings.Contains(uResults[0].Content[0].Text, "golang generics") {
		t.Fatalf("tool_result summary missing query: %+v", uResults[0].Content)
	}

	// Full ClaudeToKiro must keep the active tool turn (history tool_use ↔ current toolResults).
	payload := ClaudeToKiro(req, false)
	if payload == nil {
		t.Fatal("nil payload")
	}
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) == 0 {
		t.Fatal("ClaudeToKiro dropped tool_results after appendSearchRound")
	}
	if ctx.ToolResults[0].ToolUseID != "toolu_ws_1" {
		t.Fatalf("tool result id = %q", ctx.ToolResults[0].ToolUseID)
	}
	// History last assistant must carry matching tool_use.
	hist := payload.ConversationState.History
	if len(hist) == 0 {
		t.Fatal("expected history")
	}
	last := hist[len(hist)-1]
	if last.AssistantResponseMessage == nil || len(last.AssistantResponseMessage.ToolUses) == 0 {
		t.Fatalf("last history assistant missing tool_use: %+v", last.AssistantResponseMessage)
	}
	if last.AssistantResponseMessage.ToolUses[0].ToolUseID != "toolu_ws_1" {
		t.Fatalf("history tool_use id = %q", last.AssistantResponseMessage.ToolUses[0].ToolUseID)
	}
}

func TestExtractClaudeContent_MapSliceShape(t *testing.T) {
	// Defense in depth: []map[string]interface{} must work even without []interface{}.
	user := []map[string]interface{}{
		{"type": "tool_result", "tool_use_id": "id1", "content": "ok"},
	}
	_, _, results := extractClaudeUserContent(user)
	if len(results) != 1 || results[0].ToolUseID != "id1" {
		t.Fatalf("map-slice user content: %+v", results)
	}
	asst := []map[string]interface{}{
		{"type": "text", "text": "hi"},
		{"type": "tool_use", "id": "id1", "name": "web_search", "input": map[string]interface{}{"query": "q"}},
	}
	text, uses := extractClaudeAssistantContent(asst)
	if text != "hi" || len(uses) != 1 || uses[0].Name != "web_search" {
		t.Fatalf("map-slice assistant content: text=%q uses=%+v", text, uses)
	}
}

func TestParseSearchResults_EmptyResultsArrayIsValid(t *testing.T) {
	// Empty results is a legitimate MCP success; only unparseable/error payloads are nil.
	inner := `{"results":[],"totalResults":0,"query":"q"}`
	mcpResp := &McpResponse{
		Result: &McpResult{
			Content: []McpContent{{Type: "text", Text: inner}},
		},
	}
	results := parseSearchResults(mcpResp)
	if results == nil {
		t.Fatal("empty results array must parse as non-nil success")
	}
	if len(results.Results) != 0 {
		t.Fatalf("results len = %d", len(results.Results))
	}
}

// Some third-party clients send web_search on every turn, so all of their
// turns take the loop. The loop
// counted only the final content blocks as output, dropping the reasoning that
// Kiro bills (live: 184 output tokens reported, ~3,450 implied by credits).
func TestWebSearchLoopCountsThinkingAsOutput(t *testing.T) {
	reasoning := strings.Repeat("Let me weigh the options carefully. ", 120)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "reasoningContentEvent", map[string]interface{}{"text": reasoning}))
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "done"}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("ws-output")[:1]...)

	body, _ := json.Marshal(map[string]interface{}{
		"model": "claude-sonnet-4.5", "max_tokens": 4000,
		"thinking": map[string]interface{}{"type": "enabled", "budget_tokens": 2000},
		"tools": []interface{}{
			map[string]interface{}{"type": "web_search_20250305", "name": "web_search"},
			map[string]interface{}{"name": "read_file", "description": "Read a file", "input_schema": map[string]interface{}{"type": "object"}},
		},
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": "What changed?"}},
	})
	rec := httptest.NewRecorder()
	h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
	var resp struct {
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, rec.Body.String())
	}
	if min := estimateApproxTokens(reasoning); resp.Usage.OutputTokens < min {
		t.Fatalf("output_tokens = %d, want at least the %d reasoning tokens", resp.Usage.OutputTokens, min)
	}
}

// Kiro reasons and bills it even when the client did not ask for thinking
// (live: a no-thinking SDK client billed 54% of the credit-implied output).
// The reasoning stays out of the response but counts as output.
func TestHiddenReasoningCountsAsOutput(t *testing.T) {
	reasoning := strings.Repeat("Hidden chain of reasoning about the request. ", 120)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "reasoningContentEvent", map[string]interface{}{"text": reasoning}))
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "done"}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("hidden-reasoning")[:1]...)
	min := estimateApproxTokens(reasoning)
	outputRe := regexp.MustCompile(`"(?:output_tokens|completion_tokens)":(\d+)`)

	cases := []struct {
		name   string
		path   string
		stream bool
		serve  func(http.ResponseWriter, *http.Request)
	}{
		{"claude stream", "/v1/messages", true, h.handleClaudeMessages},
		{"claude non-stream", "/v1/messages", false, h.handleClaudeMessages},
		{"openai stream", "/v1/chat/completions", true, h.handleOpenAIChat},
		{"openai non-stream", "/v1/chat/completions", false, h.handleOpenAIChat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]interface{}{
				"model": "claude-sonnet-4.5", "max_tokens": 4000, "stream": tc.stream,
				"messages": []interface{}{map[string]interface{}{"role": "user", "content": "What changed?"}},
			})
			rec := httptest.NewRecorder()
			tc.serve(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(string(body))))
			out := rec.Body.String()
			if strings.Contains(out, "Hidden chain of reasoning") {
				t.Fatalf("reasoning leaked to a client that did not ask for thinking:\n%s", out)
			}
			best := 0
			for _, m := range outputRe.FindAllStringSubmatch(out, -1) {
				if n, _ := strconv.Atoi(m[1]); n > best {
					best = n
				}
			}
			if best < min {
				t.Fatalf("output tokens = %d, want at least the %d reasoning tokens\n%s", best, min, out)
			}
		})
	}
}

func TestReasoningProbeHeaderReplacesModelRequestFields(t *testing.T) {
	var got map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Fields map[string]interface{} `json:"additionalModelRequestFields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body.Fields
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "done"}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("reasoning-probe")[:1]...)

	send := func(header string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
			`{"model":"claude-sonnet-4.5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`))
		if header != "" {
			req.Header.Set("X-Kiro-Go-Reasoning", header)
		}
		h.handleClaudeMessages(httptest.NewRecorder(), req)
	}
	send(`{"thinking":{"type":"disabled"}}`)
	if th, _ := got["thinking"].(map[string]interface{}); th["type"] != "disabled" {
		t.Fatalf("probe fields not sent verbatim, got %v", got)
	}
	send("not json")
	if got != nil {
		t.Fatalf("an invalid probe header must be ignored, got %v", got)
	}
}

// Third-party clients that send web_search on every turn take the loop each
// turn. Via Sub2API there is no session or API key, so the loop must key affinity by the
// conversation like the plain path; otherwise each turn rotates accounts and
// misses Kiro's per-account cache (live: conv f4392c26 moved accounts).
func TestWebSearchLoopKeepsConversationOnOneAccount(t *testing.T) {
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "done"}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("ws-affinity")...)
	h.affinity = newAccountAffinity()

	turn := func(messages []interface{}) {
		body, _ := json.Marshal(map[string]interface{}{
			"model": "claude-sonnet-4.5", "max_tokens": 100,
			"tools": []interface{}{
				map[string]interface{}{"type": "web_search_20250305", "name": "web_search"},
				map[string]interface{}{"name": "read_file", "description": "Read a file", "input_schema": map[string]interface{}{"type": "object"}},
			},
			"messages": messages,
		})
		h.handleClaudeMessages(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
	}
	first := map[string]interface{}{"role": "user", "content": "What changed?"}
	turn([]interface{}{first})
	turn([]interface{}{first,
		map[string]interface{}{"role": "assistant", "content": "done"},
		map[string]interface{}{"role": "user", "content": "And then?"}})
	turn([]interface{}{first,
		map[string]interface{}{"role": "assistant", "content": "done"},
		map[string]interface{}{"role": "user", "content": "And then?"},
		map[string]interface{}{"role": "assistant", "content": "done"},
		map[string]interface{}{"role": "user", "content": "Anything else?"}})

	if len(tokens) != 3 {
		t.Fatalf("upstream calls = %d, want 3", len(tokens))
	}
	for i, tok := range tokens {
		if tok != tokens[0] {
			t.Fatalf("turn %d used %q, turn 1 used %q; a conversation must stay on one account", i+1, tok, tokens[0])
		}
	}
}

// Each loop round is a separate Kiro call with the prompt so far, billed and
// cached by Kiro on its own. The loop reported only the last round's input,
// split against the cache as it stood before the turn (live: 3 rounds, 2.12
// credits, billed as one cold 30k-token write).
func TestWebSearchLoopBillsInputPerRound(t *testing.T) {
	oldMcp := callMcpAPIFunc
	callMcpAPIFunc = func(*config.Account, *McpRequest) (*McpResponse, error) {
		inner := `{"results":[{"title":"T1","url":"https://e.com","snippet":"s1","id":"1","domain":"e.com"}],"totalResults":1,"query":"q"}`
		return &McpResponse{Result: &McpResult{Content: []McpContent{{Type: "text", Text: inner}}}}, nil
	}
	defer func() { callMcpAPIFunc = oldMcp }()

	contextPct := []float64{2.0, 2.5}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if calls == 0 {
			_, _ = w.Write(awsEventStreamFrame(t, "toolUseEvent", map[string]interface{}{
				"toolUseId": "toolu_ws1", "name": "web_search", "input": `{"query":"latest release"}`, "stop": true,
			}))
		} else {
			_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "done"}))
		}
		_, _ = w.Write(awsEventStreamFrame(t, "contextUsageEvent", map[string]interface{}{"contextUsagePercentage": contextPct[calls]}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
		calls++
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("ws-rounds")[:1]...)

	body, _ := json.Marshal(map[string]interface{}{
		"model": "claude-sonnet-4.5", "max_tokens": 100,
		"tools": []interface{}{
			map[string]interface{}{"type": "web_search_20250305", "name": "web_search"},
			map[string]interface{}{"name": "read_file", "description": "Read a file", "input_schema": map[string]interface{}{"type": "object"}},
		},
		"messages": []interface{}{map[string]interface{}{"role": "user", "content": strings.Repeat("Summarize the release notes below. ", 400)}},
	})
	rec := httptest.NewRecorder()
	h.handleClaudeMessages(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
	if calls != 2 {
		t.Fatalf("upstream calls = %d, want 2 (search round + answer round)\n%s", calls, rec.Body.String())
	}
	var resp struct {
		Usage struct {
			Input    int `json:"input_tokens"`
			Creation int `json:"cache_creation_input_tokens"`
			Read     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, rec.Body.String())
	}
	window := getContextWindowSize("claude-sonnet-4.5")
	want := int(contextPct[0]*float64(window)/100) + int(contextPct[1]*float64(window)/100)
	u := resp.Usage
	if got := u.Input + u.Creation + u.Read; got != want {
		t.Fatalf("billed input %d (in %d, write %d, read %d), want both rounds' context %d", got, u.Input, u.Creation, u.Read, want)
	}
	if u.Read == 0 {
		t.Fatalf("round 2 must read the prefix round 1 cached, got read 0 (write %d)", u.Creation)
	}
}
