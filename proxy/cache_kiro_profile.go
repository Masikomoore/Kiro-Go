package proxy

import "encoding/json"

// Simulated prompt cache that follows the Kiro payload.
//
// Kiro caches prompt prefixes per account but reports no cache token counts
// (no tokenUsage on the IDE endpoint or the CLI runtime, with or without a
// cachePoint). The client-side profile hashed the client's messages, so it
// kept reporting hits when kiro-go itself changed what upstream saw: a
// shifted truncation boundary, a flattened tool turn, another account. On
// CA2 that over-reported 5 of 13 WorkBuddy turns that Kiro billed at full
// price. This profile hashes the history actually sent instead: the prelude
// (model and tool specs) and each history entry, with a cache boundary after
// every one. The current message is the turn's new input and is never cached.
// Entries are hashed byte for byte, tool IDs included, as upstream sees them.

// kiroImageTokenEstimate approximates one history image; Kiro bills images
// by size, and history images are rare after payload shrinking.
const kiroImageTokenEstimate = 1600

// BuildKiroProfile returns the cache profile of payload. Scope is left empty,
// so entries are keyed by the upstream account like Kiro's own cache.
//
// includeCurrent adds the current message as a final boundary. It is set when
// the client asked for that write with cache_control (Claude Code marks its
// newest message), so the new turn is reported as cache creation, as on the
// Anthropic API, instead of plain input. Kiro writes it to its cache either way.
func (t *promptCacheTracker) BuildKiroProfile(payload *KiroPayload, model string, includeCurrent bool) *promptCacheProfile {
	if t == nil || payload == nil {
		return nil
	}
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	prelude := map[string]interface{}{"kind": "kiro_prelude", "model": model}
	preludeTokens := 0
	if ctx := current.UserInputMessageContext; ctx != nil && len(ctx.Tools) > 0 {
		prelude["tools"] = ctx.Tools
		preludeTokens = estimateJSONTokens(ctx.Tools)
	}
	blocks := []cacheablePromptBlock{{Value: prelude, Tokens: preludeTokens, TTL: defaultPromptCacheTTL}}
	for _, entry := range payload.ConversationState.History {
		blocks = append(blocks, cacheablePromptBlock{
			Value:        kiroEntryFingerprintValue(entry),
			Tokens:       kiroEntryTokens(entry),
			TTL:          defaultPromptCacheTTL,
			IsMessageEnd: true,
		})
	}
	currentEntry := KiroHistoryMessage{UserInputMessage: &current}
	currentTokens := kiroEntryTokens(currentEntry)
	if includeCurrent {
		blocks = append(blocks, cacheablePromptBlock{
			Value:        kiroEntryFingerprintValue(currentEntry),
			Tokens:       currentTokens,
			TTL:          defaultPromptCacheTTL,
			IsMessageEnd: true,
		})
	}
	// The split ratio must use the same basis as the cached prefix: what Kiro
	// receives. The client estimate also counts content kiro-go does not send
	// (thinking blocks, for one), which showed up as ~63k tokens of phantom
	// plain input on every Claude Code turn.
	kiroTotal := 0
	for _, b := range blocks {
		kiroTotal += b.Tokens
	}
	if !includeCurrent {
		kiroTotal += currentTokens
	}
	profile := t.profileFromBlocks(model, kiroTotal, blocks, false)
	if profile != nil {
		profile.Kiro = true
	}
	return profile
}

// kiroEntryFingerprintValue is the entry exactly as serialized upstream.
func kiroEntryFingerprintValue(entry KiroHistoryMessage) interface{} {
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil
	}
	return string(raw)
}

// kiroEntryTokens estimates an entry with the request estimator's text rules,
// so the cached share stays proportional to the reported input total.
func kiroEntryTokens(entry KiroHistoryMessage) int {
	total := 0
	if u := entry.UserInputMessage; u != nil {
		total += estimateApproxTokens(u.Content)
		total += len(u.Images) * kiroImageTokenEstimate
		if ctx := u.UserInputMessageContext; ctx != nil {
			for _, r := range ctx.ToolResults {
				for _, c := range r.Content {
					total += estimateApproxTokens(c.Text)
				}
			}
			if len(ctx.Tools) > 0 {
				total += estimateJSONTokens(ctx.Tools)
			}
		}
	}
	if a := entry.AssistantResponseMessage; a != nil {
		total += estimateApproxTokens(a.Content)
		for _, tu := range a.ToolUses {
			total += estimateApproxTokens(tu.Name) + estimateJSONTokens(tu.Input)
		}
	}
	return total
}
