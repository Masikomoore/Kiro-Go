package proxy

// Prompt-cache simulation.
//
// Kiro returns no cache token counts (meteringEvent only carries credits), so
// Anthropic's cache_creation/cache_read usage is simulated here from request
// prefixes. A hit does not change Kiro credits; the numbers only shape how a
// downstream gateway such as Sub2API bills the request.
//
// Design (compared with kiro.rs and the ikik gateway):
//   - Scope: the Claude Code session when known, else the Kiro-Go API key,
//     else the upstream account. Keying by account alone made round-robin
//     rotation miss almost every history prefix and bill creation again.
//   - Caching starts only at an explicit cache_control, as in Anthropic.
//   - Counts are estimated, then split against the real input total so
//     input + creation + read always equals what the upstream reported.
//   - Entries are written only after a successful response, bounded by an
//     LRU cap, and persisted to the data directory.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kiro-go/config"
	"kiro-go/logger"
)

const defaultPromptCacheTTL = 5 * time.Minute

// Anthropic requires cached prefixes to reach a minimum token count before
// caching takes effect. Breakpoints below this threshold are excluded from
// matching and storage to avoid reporting unrealistic 100% cache hits on
// short requests.
const defaultMinCacheableTokens = 1024
const opusMinCacheableTokens = 4096

// promptCacheLookback bounds how many earlier breakpoints a request checks,
// mirroring Anthropic's ~20-block lookback.
const promptCacheLookback = 20

const promptCachePersistInterval = time.Minute

// promptCacheUsage is the simulated usage in estimate units. split converts it
// to the real input total reported upstream.
type promptCacheUsage struct {
	CacheCreationInputTokens   int
	CacheReadInputTokens       int
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
	// PromptTotalTokens is the estimate of the whole prompt; zero means the
	// counts above are already in the same units as the reported total.
	PromptTotalTokens int
}

// promptCacheSplit is usage in reported units: Input+Creation+Read equals the
// total passed to split, and Creation5m+Creation1h equals Creation.
type promptCacheSplit struct {
	Input      int
	Creation   int
	Read       int
	Creation5m int
	Creation1h int
}

func (u promptCacheUsage) split(total int) promptCacheSplit {
	if total < 0 {
		total = 0
	}
	covered := u.CacheCreationInputTokens + u.CacheReadInputTokens
	base := u.PromptTotalTokens
	if base <= 0 {
		base = total
	}
	if covered <= 0 || base <= 0 {
		return promptCacheSplit{Input: total}
	}
	ratio := math.Min(1, math.Max(0, float64(covered)/float64(base)))
	cacheTotal := minInt(int(math.Round(float64(total)*ratio)), total)
	read := int(math.Round(float64(cacheTotal) * float64(u.CacheReadInputTokens) / float64(covered)))
	read = minInt(maxInt(read, 0), cacheTotal)
	out := promptCacheSplit{Input: total - cacheTotal, Read: read, Creation: cacheTotal - read}
	if out.Creation > 0 {
		estCreation := u.CacheCreation5mInputTokens + u.CacheCreation1hInputTokens
		if estCreation > 0 {
			out.Creation1h = int(math.Round(float64(out.Creation) * float64(u.CacheCreation1hInputTokens) / float64(estCreation)))
		}
		out.Creation5m = out.Creation - out.Creation1h
	}
	return out
}

type promptCacheBreakpoint struct {
	Fingerprint      [32]byte
	CumulativeTokens int
	TTL              time.Duration
}

type promptCacheProfile struct {
	Breakpoints      []promptCacheBreakpoint
	TotalInputTokens int
	Model            string
	// Scope isolates cache entries, e.g. "sess:<id>" or "key:<id>". Empty
	// means the upstream account chosen for the request.
	Scope string
}

func minCacheableTokensForModel(model string) int {
	lower := strings.ToLower(model)
	if strings.Contains(lower, "opus") {
		return opusMinCacheableTokens
	}
	return defaultMinCacheableTokens
}

type promptCacheEntry struct {
	ExpiresAt time.Time
	TTL       time.Duration
	LastUsed  time.Time
}

type promptCacheTracker struct {
	mu              sync.Mutex
	entriesByScope  map[string]map[[32]byte]promptCacheEntry
	maxSupportedTTL time.Duration
	dirty           bool
	persistPath     string
}

func newPromptCacheTracker(maxTTL time.Duration) *promptCacheTracker {
	if maxTTL <= 0 {
		maxTTL = defaultPromptCacheTTL
	}
	return &promptCacheTracker{
		entriesByScope:  make(map[string]map[[32]byte]promptCacheEntry),
		maxSupportedTTL: maxTTL,
	}
}

// claudeCodeSessionID extracts the session from metadata.user_id: a JSON
// string with session_id (Claude Code 2.1.78+, Sub2API) or the legacy
// "..._session_<uuid>" form.
func claudeCodeSessionID(userID string) string {
	trimmed := strings.TrimSpace(userID)
	if strings.HasPrefix(trimmed, "{") {
		var parsed struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(trimmed), &parsed) == nil && strings.TrimSpace(parsed.SessionID) != "" {
			return strings.TrimSpace(parsed.SessionID)
		}
	}
	if i := strings.Index(trimmed, "_session_"); i >= 0 {
		return strings.TrimSpace(trimmed[i+len("_session_"):])
	}
	return ""
}

// promptCacheScope picks the isolation key before an account is chosen. An
// empty result means "the upstream account".
func promptCacheScope(req *ClaudeRequest, r *http.Request, apiKeyID string) string {
	if config.GetCompatConfig().PromptCacheScope == config.PromptCacheScopeAccount {
		return ""
	}
	return accountAffinityKey(req, r, apiKeyID)
}

func (p *promptCacheProfile) scopeKey(accountID string) string {
	if p.Scope != "" {
		return p.Scope
	}
	if accountID == "" {
		return ""
	}
	return "acct:" + accountID
}

func (t *promptCacheTracker) BuildClaudeProfile(req *ClaudeRequest, totalInputTokens int) *promptCacheProfile {
	compat := config.GetCompatConfig()
	if req == nil || compat.PromptCacheMode == config.PromptCacheModeOff || !compat.SimulateAnthropicCache {
		return nil
	}
	return t.profileFromBlocks(req.Model, totalInputTokens, flattenClaudeCacheBlocks(req))
}

// BuildOpenAIProfile simulates a read-only prefix cache for Chat Completions
// and Responses requests, which do not send cache_control. The switch defaults
// off. A hit does not change what Kiro charges.
func (t *promptCacheTracker) BuildOpenAIProfile(req *OpenAIRequest, totalInputTokens int) *promptCacheProfile {
	if req == nil || !config.GetCompatConfig().SimulateOpenAICache {
		return nil
	}
	return t.profileFromBlocks(req.Model, totalInputTokens, flattenOpenAICacheBlocks(req))
}

func (t *promptCacheTracker) profileFromBlocks(model string, totalInputTokens int, blocks []cacheablePromptBlock) *promptCacheProfile {
	if len(blocks) == 0 {
		return nil
	}

	hasher := sha256.New()
	breakpoints := make([]promptCacheBreakpoint, 0)
	cumulativeTokens := 0
	var activeTTL time.Duration

	for _, block := range blocks {
		canonical := canonicalizeCacheValue(block.Value)
		writeHashChunk(hasher, canonical)
		cumulativeTokens += block.Tokens

		// Determine whether this block acts as a cache breakpoint:
		//   1) Explicit cache_control on the block itself.
		//   2) Once any explicit breakpoint has been seen, every message-end
		//      boundary becomes an implicit breakpoint so that multi-turn
		//      conversations can hit earlier stored prefixes.
		breakpointTTL := time.Duration(0)
		if block.TTL > 0 {
			breakpointTTL = block.TTL
			activeTTL = block.TTL
		} else if block.IsMessageEnd && activeTTL > 0 {
			breakpointTTL = activeTTL
		}

		if breakpointTTL <= 0 {
			continue
		}

		var fingerprint [32]byte
		copy(fingerprint[:], hasher.Sum(nil))
		breakpoints = append(breakpoints, promptCacheBreakpoint{
			Fingerprint:      fingerprint,
			CumulativeTokens: cumulativeTokens,
			TTL:              breakpointTTL,
		})
	}

	if len(breakpoints) == 0 {
		return nil
	}

	if totalInputTokens < cumulativeTokens {
		totalInputTokens = cumulativeTokens
	}

	return &promptCacheProfile{
		Breakpoints:      breakpoints,
		TotalInputTokens: totalInputTokens,
		Model:            model,
	}
}

// Compute reports the simulated usage for a request about to be served by
// accountID. Nothing is written; Update commits after a successful response.
func (t *promptCacheTracker) Compute(accountID string, profile *promptCacheProfile) promptCacheUsage {
	if t == nil || profile == nil || len(profile.Breakpoints) == 0 {
		return promptCacheUsage{}
	}
	scope := profile.scopeKey(accountID)
	if scope == "" {
		return promptCacheUsage{}
	}

	minTokens := minCacheableTokensForModel(profile.Model)
	last := profile.Breakpoints[len(profile.Breakpoints)-1]
	lastTokens := minInt(last.CumulativeTokens, profile.TotalInputTokens)
	if lastTokens < minTokens {
		return promptCacheUsage{PromptTotalTokens: profile.TotalInputTokens}
	}
	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(now)

	matchedTokens := 0
	entries := t.entriesByScope[scope]
	checked := 0
	for i := len(profile.Breakpoints) - 1; i >= 0 && checked < promptCacheLookback && len(entries) > 0; i-- {
		breakpoint := profile.Breakpoints[i]
		// Skip breakpoints below the minimum cacheable token threshold.
		if breakpoint.CumulativeTokens < minTokens {
			continue
		}
		checked++
		entry, ok := entries[breakpoint.Fingerprint]
		if !ok || !entry.ExpiresAt.After(now) {
			continue
		}
		matchedTokens = minInt(breakpoint.CumulativeTokens, lastTokens)
		break
	}

	usage := promptCacheUsage{
		CacheCreationInputTokens: maxInt(lastTokens-matchedTokens, 0),
		CacheReadInputTokens:     matchedTokens,
		PromptTotalTokens:        profile.TotalInputTokens,
	}
	usage.CacheCreation5mInputTokens, usage.CacheCreation1hInputTokens = computePromptCacheTTLBreakdown(profile, matchedTokens, lastTokens)
	if config.GetCompatConfig().PromptCacheMode == config.PromptCacheModeReadOnly {
		// Misses count as plain input instead of the cache-write premium.
		usage.CacheCreationInputTokens = 0
		usage.CacheCreation5mInputTokens = 0
		usage.CacheCreation1hInputTokens = 0
	}
	return usage
}

// Update records the request's prefixes after a successful response, and
// refreshes the TTL of prefixes already present.
func (t *promptCacheTracker) Update(accountID string, profile *promptCacheProfile) {
	if t == nil || profile == nil || len(profile.Breakpoints) == 0 {
		return
	}
	scope := profile.scopeKey(accountID)
	if scope == "" {
		return
	}

	minTokens := minCacheableTokensForModel(profile.Model)
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpiredLocked(now)

	entries := t.entriesByScope[scope]
	if entries == nil {
		entries = make(map[[32]byte]promptCacheEntry)
		t.entriesByScope[scope] = entries
	}

	for _, breakpoint := range profile.Breakpoints {
		// Skip breakpoints below the minimum cacheable token threshold.
		if breakpoint.CumulativeTokens < minTokens {
			continue
		}
		entries[breakpoint.Fingerprint] = promptCacheEntry{
			ExpiresAt: now.Add(breakpoint.TTL),
			TTL:       breakpoint.TTL,
			LastUsed:  now,
		}
		t.dirty = true
	}
	t.evictOverCapacityLocked()
}

func (t *promptCacheTracker) pruneExpiredLocked(now time.Time) {
	for scope, entries := range t.entriesByScope {
		for fingerprint, entry := range entries {
			if !entry.ExpiresAt.After(now) {
				delete(entries, fingerprint)
				t.dirty = true
			}
		}
		if len(entries) == 0 {
			delete(t.entriesByScope, scope)
		}
	}
}

// evictOverCapacityLocked drops the least recently used entries once the total
// exceeds the configured cap, down to 90% of it so eviction is not run on
// every write.
func (t *promptCacheTracker) evictOverCapacityLocked() {
	limit := config.GetCompatConfig().PromptCacheMaxEntries
	total := 0
	for _, entries := range t.entriesByScope {
		total += len(entries)
	}
	if limit <= 0 || total <= limit {
		return
	}
	type victim struct {
		scope string
		fp    [32]byte
		used  time.Time
	}
	all := make([]victim, 0, total)
	for scope, entries := range t.entriesByScope {
		for fp, entry := range entries {
			all = append(all, victim{scope, fp, entry.LastUsed})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used.Before(all[j].used) })
	for _, v := range all[:total-limit*9/10] {
		delete(t.entriesByScope[v.scope], v.fp)
		if len(t.entriesByScope[v.scope]) == 0 {
			delete(t.entriesByScope, v.scope)
		}
	}
	t.dirty = true
}

type persistedPromptCacheEntry struct {
	Scope       string `json:"scope"`
	Fingerprint string `json:"fp"`
	ExpiresAt   int64  `json:"expiresAt"`
	TTLSeconds  int64  `json:"ttl"`
	LastUsed    int64  `json:"lastUsed"`
}

// save writes unexpired entries to path when anything changed.
func (t *promptCacheTracker) save(path string) error {
	t.mu.Lock()
	if !t.dirty {
		t.mu.Unlock()
		return nil
	}
	now := time.Now()
	t.pruneExpiredLocked(now)
	out := make([]persistedPromptCacheEntry, 0)
	for scope, entries := range t.entriesByScope {
		for fp, entry := range entries {
			out = append(out, persistedPromptCacheEntry{
				Scope:       scope,
				Fingerprint: hex.EncodeToString(fp[:]),
				ExpiresAt:   entry.ExpiresAt.Unix(),
				TTLSeconds:  int64(entry.TTL / time.Second),
				LastUsed:    entry.LastUsed.Unix(),
			})
		}
	}
	t.dirty = false
	t.mu.Unlock()

	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// load reads entries saved by save, skipping expired ones.
func (t *promptCacheTracker) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var in []persistedPromptCacheEntry
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range in {
		expires := time.Unix(e.ExpiresAt, 0)
		raw, err := hex.DecodeString(e.Fingerprint)
		if err != nil || len(raw) != 32 || e.Scope == "" || !expires.After(now) {
			continue
		}
		var fp [32]byte
		copy(fp[:], raw)
		entries := t.entriesByScope[e.Scope]
		if entries == nil {
			entries = make(map[[32]byte]promptCacheEntry)
			t.entriesByScope[e.Scope] = entries
		}
		entries[fp] = promptCacheEntry{ExpiresAt: expires, TTL: time.Duration(e.TTLSeconds) * time.Second, LastUsed: time.Unix(e.LastUsed, 0)}
	}
	return nil
}

// startPersistence loads saved entries and saves changes every minute.
func (t *promptCacheTracker) startPersistence(path string, stop <-chan struct{}) {
	if t == nil || path == "" {
		return
	}
	t.persistPath = path
	if err := t.load(path); err != nil {
		logger.Warnf("[PromptCache] Failed to load %s: %v", path, err)
	}
	go func() {
		ticker := time.NewTicker(promptCachePersistInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.flush()
			case <-stop:
				t.flush()
				return
			}
		}
	}()
}

// flush saves pending changes to the persistence file, if one is set.
func (t *promptCacheTracker) flush() {
	if t == nil || t.persistPath == "" {
		return
	}
	if err := t.save(t.persistPath); err != nil {
		logger.Warnf("[PromptCache] Failed to save %s: %v", t.persistPath, err)
	}
}

type cacheablePromptBlock struct {
	Value        interface{}
	Tokens       int
	TTL          time.Duration
	IsMessageEnd bool
}

func flattenClaudeCacheBlocks(req *ClaudeRequest) []cacheablePromptBlock {
	blocks := make([]cacheablePromptBlock, 0)
	blocks = append(blocks, buildCachePreludeBlock(req))

	for toolIndex, tool := range req.Tools {
		toolValue := map[string]interface{}{
			"kind":         "tool",
			"tool_index":   toolIndex,
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.InputSchema,
		}
		fingerprintValue := stripCachePositionKeys(toolValue)
		blocks = append(blocks, cacheablePromptBlock{
			Value:  fingerprintValue,
			Tokens: estimateApproxTokens(tool.Name) + estimateApproxTokens(tool.Description) + estimateJSONTokens(tool.InputSchema),
			TTL:    normalizePromptCacheTTL(extractPromptCacheTTL(tool)),
		})
	}

	appendSystemCacheBlocks(&blocks, req.System)

	for messageIndex, msg := range req.Messages {
		appendMessageCacheBlocks(&blocks, messageIndex, msg)
	}

	return blocks
}

func flattenOpenAICacheBlocks(req *OpenAIRequest) []cacheablePromptBlock {
	blocks := []cacheablePromptBlock{{
		Value: map[string]interface{}{
			"kind":  "openai_prelude",
			"model": req.Model,
		},
	}}
	for toolIndex, tool := range req.Tools {
		blocks = append(blocks, cacheablePromptBlock{
			Value: map[string]interface{}{
				"kind":        "openai_tool",
				"tool_index":  toolIndex,
				"name":        tool.Function.Name,
				"description": tool.Function.Description,
				"parameters":  tool.Function.Parameters,
			},
			Tokens: estimateApproxTokens(tool.Function.Name) + estimateApproxTokens(tool.Function.Description) + estimateJSONTokens(tool.Function.Parameters),
			TTL:    defaultPromptCacheTTL,
		})
	}
	for messageIndex, msg := range req.Messages {
		content := openAICacheContent(msg.Content)
		calls := make([]map[string]string, 0, len(msg.ToolCalls))
		tokens := estimateOpenAIContentTokens(content)
		for _, call := range msg.ToolCalls {
			calls = append(calls, map[string]string{
				"name":      call.Function.Name,
				"arguments": call.Function.Arguments,
			})
			tokens += estimateApproxTokens(call.Function.Name) + estimateApproxTokens(call.Function.Arguments)
		}
		blocks = append(blocks, cacheablePromptBlock{
			Value: map[string]interface{}{
				"kind":          "openai_message",
				"message_index": messageIndex,
				"role":          msg.Role,
				"content":       content,
				"tool_calls":    calls,
			},
			Tokens:       tokens,
			TTL:          defaultPromptCacheTTL,
			IsMessageEnd: true,
		})
	}
	return blocks
}

// openAICacheContent drops reasoning parts. Their text changes every turn and
// would make an otherwise stable prefix miss. tool_call ids are omitted by the
// caller for the same reason.
func openAICacheContent(content interface{}) interface{} {
	parts, ok := content.([]interface{})
	if !ok {
		return content
	}
	kept := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		block, ok := part.(map[string]interface{})
		if !ok {
			kept = append(kept, part)
			continue
		}
		kind, _ := block["type"].(string)
		switch strings.ToLower(kind) {
		case "reasoning", "reasoning_text", "reasoning_content":
			continue
		default:
			kept = append(kept, part)
		}
	}
	return kept
}

func buildCachePreludeBlock(req *ClaudeRequest) cacheablePromptBlock {
	prelude := map[string]interface{}{
		"kind":        "request_prelude",
		"model":       req.Model,
		"tool_choice": req.ToolChoice,
	}
	// The prelude only separates models and tool choices; the request
	// estimate does not count it, so neither does the cache.
	return cacheablePromptBlock{Value: prelude}
}

func appendSystemCacheBlocks(blocks *[]cacheablePromptBlock, system interface{}) {
	switch v := system.(type) {
	case string:
		appendPromptBlock(blocks, map[string]interface{}{
			"kind":         "system",
			"system_index": 0,
			"block": map[string]interface{}{
				"type": "text",
				"text": v,
			},
		}, false)
	case []interface{}:
		for i, block := range v {
			appendPromptBlock(blocks, map[string]interface{}{
				"kind":         "system",
				"system_index": i,
				"block":        block,
			}, false)
		}
	case []string:
		for i, block := range v {
			appendPromptBlock(blocks, map[string]interface{}{
				"kind":         "system",
				"system_index": i,
				"block": map[string]interface{}{
					"type": "text",
					"text": block,
				},
			}, false)
		}
	}
}

func appendMessageCacheBlocks(blocks *[]cacheablePromptBlock, messageIndex int, msg ClaudeMessage) {
	role := msg.Role
	switch content := msg.Content.(type) {
	case string:
		appendPromptBlock(blocks, map[string]interface{}{
			"kind":          "message",
			"message_index": messageIndex,
			"role":          role,
			"block_index":   0,
			"block": map[string]interface{}{
				"type": "text",
				"text": content,
			},
		}, true)
	case []interface{}:
		lastIdx := len(content) - 1
		for blockIndex, block := range content {
			appendPromptBlock(blocks, map[string]interface{}{
				"kind":          "message",
				"message_index": messageIndex,
				"role":          role,
				"block_index":   blockIndex,
				"block":         block,
			}, blockIndex == lastIdx)
		}
	default:
		if content != nil {
			appendPromptBlock(blocks, map[string]interface{}{
				"kind":          "message",
				"message_index": messageIndex,
				"role":          role,
				"block_index":   0,
				"block":         content,
			}, true)
		}
	}
}

func appendPromptBlock(blocks *[]cacheablePromptBlock, wrapper map[string]interface{}, isMessageEnd bool) {
	blockValue := wrapper["block"]
	ttl := normalizePromptCacheTTL(extractPromptCacheTTL(blockValue))

	// Drop volatile billing metadata from the cache fingerprint. Claude Code's
	// x-anthropic-billing-header can drift, appear, or disappear across
	// otherwise identical requests, and it does not change model semantics.
	if isAnthropicBillingHeaderBlock(blockValue) {
		return
	}

	fingerprintValue := stripCachePositionKeys(wrapper)
	fingerprintValue["block"] = stripVolatileToolIDs(blockValue)
	*blocks = append(*blocks, cacheablePromptBlock{
		Value: fingerprintValue,
		// Same estimator as the request total, so the split ratio is sound.
		Tokens:       estimateClaudeValueTokens(blockValue),
		TTL:          ttl,
		IsMessageEnd: isMessageEnd,
	})
}

func stripCachePositionKeys(value map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(value))
	for key, item := range value {
		if isCachePositionKey(key) {
			continue
		}
		cloned[key] = item
	}
	return cloned
}

func isAnthropicBillingHeaderBlock(value interface{}) bool {
	blockMap, ok := value.(map[string]interface{})
	if !ok {
		return false
	}

	// Only normalize text blocks (or blocks without an explicit type but containing text).
	if t, ok := blockMap["type"].(string); ok && t != "" && t != "text" {
		return false
	}

	text, ok := blockMap["text"].(string)
	if !ok {
		return false
	}

	trimmed := strings.TrimLeft(text, " \t\r\n")
	return strings.HasPrefix(strings.ToLower(trimmed), "x-anthropic-billing-header:")
}

func extractPromptCacheTTL(value interface{}) time.Duration {
	block, ok := value.(map[string]interface{})
	if !ok {
		if raw, err := json.Marshal(value); err == nil {
			var decoded map[string]interface{}
			if json.Unmarshal(raw, &decoded) == nil {
				block = decoded
				ok = true
			}
		}
	}
	if !ok {
		return 0
	}

	rawCache, ok := block["cache_control"]
	if !ok {
		return 0
	}
	cacheControl, ok := rawCache.(map[string]interface{})
	if !ok {
		return 0
	}
	cacheType, _ := cacheControl["type"].(string)
	if !strings.EqualFold(cacheType, "ephemeral") {
		return 0
	}

	if ttl, ok := parsePromptCacheTTLValue(cacheControl["ttl"]); ok {
		return ttl
	}
	return defaultPromptCacheTTL
}

func parsePromptCacheTTLValue(value interface{}) (time.Duration, bool) {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(v))
		if trimmed == "" {
			return 0, false
		}
		if d, err := time.ParseDuration(trimmed); err == nil {
			return d, true
		}
		if seconds, err := strconv.Atoi(trimmed); err == nil {
			return time.Duration(seconds) * time.Second, true
		}
	case float64:
		if v > 0 {
			return time.Duration(v) * time.Second, true
		}
	case int:
		if v > 0 {
			return time.Duration(v) * time.Second, true
		}
	case int64:
		if v > 0 {
			return time.Duration(v) * time.Second, true
		}
	}
	return 0, false
}

func normalizePromptCacheTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return 0
	}
	if ttl > time.Hour {
		return time.Hour
	}
	if ttl > defaultPromptCacheTTL {
		return time.Hour
	}
	return defaultPromptCacheTTL
}

// computePromptCacheTTLBreakdown splits the creation range (matchedTokens,
// lastTokens] by the TTL of the breakpoints it spans.
func computePromptCacheTTLBreakdown(profile *promptCacheProfile, matchedTokens, lastTokens int) (int, int) {
	if profile == nil || len(profile.Breakpoints) == 0 || lastTokens <= matchedTokens {
		return 0, 0
	}

	cache5m := 0
	cache1h := 0
	previous := matchedTokens
	for _, breakpoint := range profile.Breakpoints {
		current := minInt(breakpoint.CumulativeTokens, lastTokens)
		if current <= previous {
			continue
		}
		delta := current - previous
		if breakpoint.TTL >= time.Hour {
			cache1h += delta
		} else {
			cache5m += delta
		}
		previous = current
	}
	return cache5m, cache1h
}

// promptCacheReport is the simulated cache usage for one finished response.
type promptCacheReport struct {
	usage   promptCacheUsage
	include bool
}

// commitPromptCache computes usage for a successful request served by
// accountID and records its prefixes.
func (h *Handler) commitPromptCache(accountID string, profile *promptCacheProfile) promptCacheReport {
	if profile == nil {
		return promptCacheReport{}
	}
	usage := h.promptCache.Compute(accountID, profile)
	h.promptCache.Update(accountID, profile)
	return promptCacheReport{usage: usage, include: true}
}

// usageMap builds a usage object. Zero cache fields are always present, as
// clients of the web_search paths expect them; webSearch adds the server
// tool counter.
func (r promptCacheReport) usageMap(inputTokens, outputTokens int, webSearch bool) map[string]interface{} {
	m := buildClaudeUsageMap(inputTokens, outputTokens, r.usage, r.include)
	if _, ok := m["cache_creation_input_tokens"]; !ok {
		m["cache_creation_input_tokens"] = 0
		m["cache_read_input_tokens"] = 0
	}
	if webSearch {
		m["server_tool_use"] = map[string]interface{}{"web_search_requests": 1}
	}
	return m
}

// billedClaudeInputTokens is the uncached part of the reported input total.
func billedClaudeInputTokens(inputTokens int, usage promptCacheUsage) int {
	return usage.split(inputTokens).Input
}

// openAICachedTokens is the read portion of an OpenAI prompt. Creation is not
// reported: these clients did not ask for cache writes, and Sub2API bills
// prompt_tokens_details.cached_tokens as a cache read inside prompt_tokens.
func openAICachedTokens(total int, usage promptCacheUsage) int {
	usage.CacheCreationInputTokens = 0
	usage.CacheCreation5mInputTokens = 0
	usage.CacheCreation1hInputTokens = 0
	read := usage.split(total).Read
	if read < 0 {
		return 0
	}
	if total >= 0 && read > total {
		return total
	}
	return read
}

func openAIUsageMap(inputTokens, outputTokens, cached int) map[string]interface{} {
	usage := map[string]interface{}{
		"prompt_tokens":     inputTokens,
		"completion_tokens": outputTokens,
		"total_tokens":      inputTokens + outputTokens,
	}
	if cached > 0 {
		usage["prompt_tokens_details"] = map[string]int{"cached_tokens": cached}
	}
	return usage
}

func (h *Handler) openAICacheProfile(req *OpenAIRequest, total int, r *http.Request, apiKeyID string) *promptCacheProfile {
	if h == nil || h.promptCache == nil {
		return nil
	}
	profile := h.promptCache.BuildOpenAIProfile(req, total)
	if profile != nil {
		profile.Scope = promptCacheScope(nil, r, apiKeyID)
	}
	return profile
}

func (h *Handler) commitOpenAICache(accountID string, profile *promptCacheProfile, inputTokens int) int {
	if h == nil || h.promptCache == nil || profile == nil {
		return 0
	}
	usage := h.promptCache.Compute(accountID, profile)
	h.promptCache.Update(accountID, profile)
	return openAICachedTokens(inputTokens, usage)
}

// buildClaudeUsageMap reports usage against the real input total: input,
// cache creation, and cache read add up to inputTokens.
func buildClaudeUsageMap(inputTokens, outputTokens int, usage promptCacheUsage, includeCache bool) map[string]interface{} {
	if !includeCache {
		return map[string]interface{}{
			"input_tokens":  maxInt(inputTokens, 0),
			"output_tokens": outputTokens,
		}
	}
	split := usage.split(inputTokens)
	return map[string]interface{}{
		"input_tokens":                split.Input,
		"output_tokens":               outputTokens,
		"cache_creation_input_tokens": split.Creation,
		"cache_read_input_tokens":     split.Read,
		"cache_creation": map[string]int{
			"ephemeral_5m_input_tokens": split.Creation5m,
			"ephemeral_1h_input_tokens": split.Creation1h,
		},
	}
}

// stripVolatileToolIDs drops tool_use.id and tool_result.tool_use_id from a
// block's fingerprint. Some clients and gateways regenerate those IDs, which
// would otherwise break every later prefix; the call and result content still
// count.
func stripVolatileToolIDs(block interface{}) interface{} {
	m, ok := block.(map[string]interface{})
	if !ok {
		return block
	}
	var drop string
	switch m["type"] {
	case "tool_use":
		drop = "id"
	case "tool_result":
		drop = "tool_use_id"
	default:
		return block
	}
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if k != drop {
			out[k] = v
		}
	}
	return out
}

func canonicalizeCacheValue(value interface{}) string {
	var buf bytes.Buffer
	writeCanonicalJSON(&buf, value)
	return buf.String()
}

func writeCanonicalJSON(buf *bytes.Buffer, value interface{}) {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case string:
		encoded, _ := json.Marshal(v)
		buf.Write(encoded)
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		encoded, _ := json.Marshal(v)
		buf.Write(encoded)
	case []interface{}:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonicalJSON(buf, item)
		}
		buf.WriteByte(']')
	case map[string]interface{}:
		buf.WriteByte('{')
		keys := make([]string, 0, len(v))
		for key := range v {
			if key == "cache_control" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			buf.Write(encoded)
			buf.WriteByte(':')
			writeCanonicalJSON(buf, v[key])
		}
		buf.WriteByte('}')
	default:
		encoded, _ := json.Marshal(v)
		buf.Write(encoded)
	}
}

func isCachePositionKey(key string) bool {
	switch key {
	case "tool_index", "system_index", "message_index", "block_index":
		return true
	default:
		return false
	}
}

func writeHashChunk(hasher hashWriter, chunk string) {
	length := strconv.Itoa(len(chunk))
	hasher.Write([]byte(length))
	hasher.Write([]byte{0})
	hasher.Write([]byte(chunk))
	hasher.Write([]byte{0})
}

type hashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
