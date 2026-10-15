package proxy

import (
	"kiro-go/config"
	"strings"
	"testing"
)

func enableStructuredToolHistory(t *testing.T) {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.UpdateCompatSettings(config.CompatSettings{StructuredToolHistory: true}); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })
}

func toolUseBlock(id, name string) map[string]interface{} {
	return map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": map[string]interface{}{"q": id}}
}

func toolResultBlock(id, text string) map[string]interface{} {
	return map[string]interface{}{"type": "tool_result", "tool_use_id": id, "content": text}
}

func structuredHistoryRequest() *ClaudeRequest {
	return &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1000,
		System:    "sys",
		Tools:     []ClaudeTool{{Name: "Bash", Description: "run", InputSchema: map[string]interface{}{"type": "object"}}},
		Messages: []ClaudeMessage{
			{Role: "user", Content: "start"},
			{Role: "assistant", Content: []interface{}{toolUseBlock("t1", "Read")}},
			{Role: "user", Content: []interface{}{toolResultBlock("t1", "file body")}},
			{Role: "assistant", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "searching"},
				toolUseBlock("t2", "mcp__search-grep"),
				toolUseBlock("t3", "Read"),
			}},
			{Role: "user", Content: []interface{}{toolResultBlock("t2", "matches")}},
		},
	}
}

func toolUseIDs(msg *KiroAssistantResponseMessage) []string {
	var ids []string
	for _, tu := range msg.ToolUses {
		ids = append(ids, tu.ToolUseID)
	}
	return ids
}

func TestStructuredToolHistoryKeepsPairsAndAddsPlaceholders(t *testing.T) {
	enableStructuredToolHistory(t)

	payload := ClaudeToKiro(structuredHistoryRequest(), false)

	history := payload.ConversationState.History
	var assistants []*KiroAssistantResponseMessage
	var resultIDs []string
	for _, h := range history {
		if h.AssistantResponseMessage != nil && len(h.AssistantResponseMessage.ToolUses) > 0 {
			assistants = append(assistants, h.AssistantResponseMessage)
		}
		if h.UserInputMessage != nil {
			if strings.Contains(h.UserInputMessage.Content, toolResultsContinuationPrefix) {
				t.Fatalf("structured mode must not narrate history tool results: %q", h.UserInputMessage.Content)
			}
			if ctx := h.UserInputMessage.UserInputMessageContext; ctx != nil {
				for _, tr := range ctx.ToolResults {
					resultIDs = append(resultIDs, tr.ToolUseID)
				}
			}
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("expected two structured assistant tool turns, got %d", len(assistants))
	}
	if got := strings.Join(toolUseIDs(assistants[0]), ","); got != "t1" {
		t.Fatalf("first tool turn = %s", got)
	}
	if got := strings.Join(toolUseIDs(assistants[1]), ","); got != "t2" {
		t.Fatalf("unanswered t3 must be removed, got %s", got)
	}
	if assistants[1].ToolUses[0].Name != "mcpSearchGrep" {
		t.Fatalf("history tool names must match the names sent to Kiro, got %q", assistants[1].ToolUses[0].Name)
	}
	if strings.Join(resultIDs, ",") != "t1" {
		t.Fatalf("history results = %v", resultIDs)
	}

	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) != 1 || ctx.ToolResults[0].ToolUseID != "t2" {
		t.Fatalf("current t2 result must be attached, got %+v", ctx)
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != structuredToolResultsContent {
		t.Fatalf("current content = %q", got)
	}
	names := map[string]bool{}
	for _, tool := range ctx.Tools {
		names[tool.ToolSpecification.Name] = true
	}
	if !names["bash"] || !names["read"] || !names["mcpSearchGrep"] {
		t.Fatalf("expected bash plus placeholders for read and mcpSearchGrep, got %v", names)
	}
	if payload.ToolNameMap["mcpSearchGrep"] != "mcp__search-grep" || payload.ToolNameMap["read"] != "Read" {
		t.Fatalf("placeholder tool name must restore to the client name, map=%v", payload.ToolNameMap)
	}
}

func TestStructuredToolHistoryNarratesOrphanCurrentResult(t *testing.T) {
	enableStructuredToolHistory(t)
	req := structuredHistoryRequest()
	req.Messages[len(req.Messages)-1] = ClaudeMessage{Role: "user", Content: []interface{}{toolResultBlock("unknown", "stray output")}}

	payload := ClaudeToKiro(req, false)

	current := payload.ConversationState.CurrentMessage.UserInputMessage
	if current.UserInputMessageContext != nil && len(current.UserInputMessageContext.ToolResults) > 0 {
		t.Fatalf("an unpaired result must not be attached: %+v", current.UserInputMessageContext.ToolResults)
	}
	if !strings.Contains(current.Content, "stray output") {
		t.Fatalf("an unpaired result must be folded into text, got %q", current.Content)
	}
}

func TestDefaultModeStillFlattensOlderToolCycles(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	payload := ClaudeToKiro(structuredHistoryRequest(), false)

	structuredTurns := 0
	for _, h := range payload.ConversationState.History {
		if h.AssistantResponseMessage != nil && len(h.AssistantResponseMessage.ToolUses) > 0 {
			structuredTurns++
		}
	}
	if structuredTurns > 1 {
		t.Fatalf("default mode keeps at most the active tool turn structured, got %d", structuredTurns)
	}
}

func TestPruneOrphanedToolPairsAfterTruncation(t *testing.T) {
	enableStructuredToolHistory(t)
	payload := &KiroPayload{}
	result := KiroHistoryMessage{UserInputMessage: &KiroUserInputMessage{
		Content: "results",
		UserInputMessageContext: &UserInputMessageContext{ToolResults: []KiroToolResult{
			{ToolUseID: "dropped", Content: []KiroResultContent{{Text: "x"}}},
		}},
	}}
	payload.ConversationState.History = []KiroHistoryMessage{userTurn("placeholder"), result, assistantTurn("done")}

	pruneOrphanedToolPairs(payload)

	if ctx := payload.ConversationState.History[1].UserInputMessage.UserInputMessageContext; ctx != nil {
		t.Fatalf("a result whose call was dropped must be removed, got %+v", ctx)
	}
}
