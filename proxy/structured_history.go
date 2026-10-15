// Structured tool history (experimental, compat.structuredToolHistory).
//
// The default path flattens every older tool cycle into text, because Kiro
// rejects history that references tools it was not told about or tool calls
// without results. kiro.rs and the ikik gateway keep the structure instead and
// satisfy those rules directly: unpaired calls and results are removed, and a
// placeholder spec is added for every tool name used in history. Kiro matches
// tool names case-insensitively. Portions adapted from hank9999/kiro.rs (MIT).
package proxy

import (
	"strings"

	"kiro-go/config"
)

const (
	historyToolPlaceholderDescription = "Tool used in conversation history"
	structuredToolResultsContent      = "Tool results provided."
)

// prepareToolHistory turns built history into what is sent upstream. It returns
// the history, the tool results to attach structurally to the current message,
// and the current results that must be folded into the message text instead.
// nameFor maps a client tool name to the name sent to Kiro.
func prepareToolHistory(history []KiroHistoryMessage, current []KiroToolResult, nameFor func(string) string) ([]KiroHistoryMessage, []KiroToolResult, []KiroToolResult) {
	if config.GetCompatConfig().StructuredToolHistory {
		structured, attach := structureKiroHistory(history, current, nameFor)
		return structured, attach, missingToolResults(current, attach)
	}
	ids := collectToolResultIDs(current)
	if currentToolResultsMatchLastAssistant(history, ids) {
		return sanitizeKiroHistory(history, ids), current, current
	}
	return sanitizeKiroHistory(history, nil), nil, current
}

func missingToolResults(all, attached []KiroToolResult) []KiroToolResult {
	if len(attached) == len(all) {
		return nil
	}
	kept := make(map[string]bool, len(attached))
	for _, tr := range attached {
		kept[tr.ToolUseID] = true
	}
	var out []KiroToolResult
	for _, tr := range all {
		if !kept[tr.ToolUseID] {
			out = append(out, tr)
		}
	}
	return out
}

// structureKiroHistory keeps tool calls and results structured in history. A
// call survives only if a result answers it, here or in the current message; a
// result survives only if it answers an earlier call and is the first answer.
func structureKiroHistory(history []KiroHistoryMessage, current []KiroToolResult, nameFor func(string) string) ([]KiroHistoryMessage, []KiroToolResult) {
	calls := make(map[string]bool)
	answered := make(map[string]bool)

	keepResults := func(results []KiroToolResult) []KiroToolResult {
		var kept []KiroToolResult
		for _, tr := range results {
			id := strings.TrimSpace(tr.ToolUseID)
			if id == "" || !calls[id] || answered[id] {
				continue
			}
			answered[id] = true
			kept = append(kept, tr)
		}
		return kept
	}

	for i := range history {
		if a := history[i].AssistantResponseMessage; a != nil {
			for j := range a.ToolUses {
				if nameFor != nil {
					a.ToolUses[j].Name = nameFor(a.ToolUses[j].Name)
				}
				calls[a.ToolUses[j].ToolUseID] = true
			}
		}
		if u := history[i].UserInputMessage; u != nil && u.UserInputMessageContext != nil {
			u.UserInputMessageContext.ToolResults = keepResults(u.UserInputMessageContext.ToolResults)
		}
	}
	attach := keepResults(current)

	out := history[:0:0]
	for _, msg := range history {
		if a := msg.AssistantResponseMessage; a != nil {
			a.Content = stripPollutedToolCallText(a.Content)
			uses := a.ToolUses[:0:0]
			for _, tu := range a.ToolUses {
				if answered[tu.ToolUseID] {
					uses = append(uses, tu)
				}
			}
			a.ToolUses = uses
			if len(a.ToolUses) == 0 {
				if c := strings.TrimSpace(a.Content); c == "" || c == minimalFallbackUserContent {
					continue
				}
			}
		}
		if u := msg.UserInputMessage; u != nil {
			hasResults := false
			if ctx := u.UserInputMessageContext; ctx != nil {
				ctx.Tools = nil
				hasResults = len(ctx.ToolResults) > 0
				if !hasResults {
					u.UserInputMessageContext = nil
				}
			}
			if strings.TrimSpace(u.Content) == "" && len(u.Images) == 0 {
				if hasResults {
					u.Content = structuredToolResultsContent
				} else {
					u.Content = minimalFallbackUserContent
				}
			}
		}
		out = append(out, msg)
	}
	return trimLeadingAssistantHistory(out), attach
}

// appendHistoryPlaceholderTools declares every tool name used in history that
// the current tool list lacks.
func appendHistoryPlaceholderTools(tools []KiroToolWrapper, history []KiroHistoryMessage) []KiroToolWrapper {
	if !config.GetCompatConfig().StructuredToolHistory {
		return tools
	}
	known := make(map[string]bool, len(tools))
	for _, t := range tools {
		known[strings.ToLower(t.ToolSpecification.Name)] = true
	}
	for _, h := range history {
		if h.AssistantResponseMessage == nil {
			continue
		}
		for _, tu := range h.AssistantResponseMessage.ToolUses {
			key := strings.ToLower(strings.TrimSpace(tu.Name))
			if key == "" || known[key] {
				continue
			}
			known[key] = true
			w := KiroToolWrapper{}
			w.ToolSpecification.Name = tu.Name
			w.ToolSpecification.Description = historyToolPlaceholderDescription
			w.ToolSpecification.InputSchema = InputSchema{JSON: map[string]interface{}{
				"type":                 "object",
				"properties":           map[string]interface{}{},
				"required":             []interface{}{},
				"additionalProperties": true,
			}}
			tools = append(tools, w)
		}
	}
	return tools
}

// pruneOrphanedToolPairs re-applies pairing after history turns were dropped
// for size: results whose call was dropped go, and so do calls whose result
// went with them.
func pruneOrphanedToolPairs(payload *KiroPayload) {
	if payload == nil || !config.GetCompatConfig().StructuredToolHistory {
		return
	}
	var current []KiroToolResult
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil {
		current = ctx.ToolResults
	}
	history, attach := structureKiroHistory(payload.ConversationState.History, current, nil)
	payload.ConversationState.History = history
	if ctx != nil {
		ctx.ToolResults = attach
	}
}
