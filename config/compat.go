package config

import (
	"fmt"
	"strings"
	"time"
)

// CompatSettings groups upstream-compatibility and prompt-cache switches as
// stored in config.json under "compat". Zero values mean "use the default", so
// an existing config file keeps working without edits.
type CompatSettings struct {
	// Cooldown for a 429 "suspicious activity ... temporary limits" response,
	// which throttles one account rather than the whole upstream. Default 10.
	AccountThrottleCooldownMinutes int `json:"accountThrottleCooldownMinutes,omitempty"`

	// Default-on switches; set to false to turn one off.
	NativeReasoningEffort *bool `json:"nativeReasoningEffort,omitempty"`
	ToolChunkingHints     *bool `json:"toolChunkingHints,omitempty"`
	ThinkingSignature     *bool `json:"thinkingSignature,omitempty"`
	ToolUseXMLFilter      *bool `json:"toolUseXmlFilter,omitempty"`
	ImageDedup            *bool `json:"imageDedup,omitempty"`
	// SchedulingMode picks the upstream account per request: sticky (default)
	// | round_robin | priority. See the Scheduling* constants.
	SchedulingMode string `json:"schedulingMode,omitempty"`

	// ImplicitAnthropicCache simulates prompt cache for /v1/messages requests
	// that carry no cache_control, using message boundaries. Default on.
	ImplicitAnthropicCache *bool `json:"implicitAnthropicCache,omitempty"`

	// CLIEndpointFirst (experimental, default off) sends OAuth accounts to the
	// Kiro CLI runtime first and falls back to the IDE endpoints on any error.
	CLIEndpointFirst bool `json:"cliEndpointFirst,omitempty"`

	// Experimental, default off.
	StructuredToolHistory   bool   `json:"structuredToolHistory,omitempty"`
	NormalizeKiroToolParams bool   `json:"normalizeKiroToolParams,omitempty"`
	ImageDownscale          bool   `json:"imageDownscale,omitempty"`
	BuilderIDPlaceholderArn string `json:"builderIdPlaceholderArn,omitempty"` // off | on_demand | always

	// Prompt-cache simulation. Mode: full (default) | read_only | off.
	// Scope: auto (session, then API key, then account; default) | account.
	PromptCacheMode       string `json:"promptCacheMode,omitempty"`
	PromptCacheScope      string `json:"promptCacheScope,omitempty"`
	PromptCacheMaxEntries int    `json:"promptCacheMaxEntries,omitempty"`

	// SimulateAnthropicCache reports simulated cache usage on /v1/messages.
	// Nil means on. It does not change what Kiro charges.
	SimulateAnthropicCache *bool `json:"simulateAnthropicCache,omitempty"`
}

const (
	PlaceholderArnOff      = "off"
	PlaceholderArnOnDemand = "on_demand"
	PlaceholderArnAlways   = "always"

	PromptCacheModeFull     = "full"
	PromptCacheModeReadOnly = "read_only"
	PromptCacheModeOff      = "off"

	PromptCacheScopeAuto    = "auto"
	PromptCacheScopeAccount = "account"

	// SchedulingSticky keeps a conversation (Claude Code session, else API
	// key) on the account that served it; new conversations rotate.
	SchedulingSticky = "sticky"
	// SchedulingRoundRobin moves every request to the next account.
	SchedulingRoundRobin = "round_robin"
	// SchedulingPriority always uses the first ready account of the best
	// priority tier, like kiro-RS-Tool's default mode.
	SchedulingPriority = "priority"

	defaultAccountThrottleCooldown = 10 * time.Minute
	defaultPromptCacheMaxEntries   = 20000
)

// CompatConfig is CompatSettings with defaults applied.
type CompatConfig struct {
	AccountThrottleCooldown time.Duration
	NativeReasoningEffort   bool
	ToolChunkingHints       bool
	ThinkingSignature       bool
	ToolUseXMLFilter        bool
	ImageDedup              bool
	StructuredToolHistory   bool
	NormalizeKiroToolParams bool
	ImageDownscale          bool
	BuilderIDPlaceholderArn string
	PromptCacheMode         string
	PromptCacheScope        string
	PromptCacheMaxEntries   int
	SimulateAnthropicCache  bool
	SchedulingMode          string
	ImplicitAnthropicCache  bool
	CLIEndpointFirst        bool
}

func boolOrDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// ResolveCompatSettings applies defaults to stored settings.
func ResolveCompatSettings(s CompatSettings) CompatConfig {
	out := CompatConfig{
		AccountThrottleCooldown: defaultAccountThrottleCooldown,
		NativeReasoningEffort:   boolOrDefault(s.NativeReasoningEffort, true),
		ToolChunkingHints:       boolOrDefault(s.ToolChunkingHints, true),
		ThinkingSignature:       boolOrDefault(s.ThinkingSignature, true),
		ToolUseXMLFilter:        boolOrDefault(s.ToolUseXMLFilter, true),
		ImageDedup:              boolOrDefault(s.ImageDedup, true),
		StructuredToolHistory:   s.StructuredToolHistory,
		NormalizeKiroToolParams: s.NormalizeKiroToolParams,
		ImageDownscale:          s.ImageDownscale,
		BuilderIDPlaceholderArn: PlaceholderArnOff,
		PromptCacheMode:         PromptCacheModeFull,
		PromptCacheScope:        PromptCacheScopeAuto,
		PromptCacheMaxEntries:   defaultPromptCacheMaxEntries,
		SimulateAnthropicCache:  boolOrDefault(s.SimulateAnthropicCache, true),
		SchedulingMode:          SchedulingSticky,
		ImplicitAnthropicCache:  boolOrDefault(s.ImplicitAnthropicCache, true),
		CLIEndpointFirst:        s.CLIEndpointFirst,
	}
	if s.AccountThrottleCooldownMinutes > 0 {
		out.AccountThrottleCooldown = time.Duration(s.AccountThrottleCooldownMinutes) * time.Minute
	}
	switch v := strings.ToLower(strings.TrimSpace(s.BuilderIDPlaceholderArn)); v {
	case PlaceholderArnOnDemand, PlaceholderArnAlways:
		out.BuilderIDPlaceholderArn = v
	}
	switch v := strings.ToLower(strings.TrimSpace(s.PromptCacheMode)); v {
	case PromptCacheModeReadOnly, PromptCacheModeOff:
		out.PromptCacheMode = v
	}
	if strings.EqualFold(strings.TrimSpace(s.PromptCacheScope), PromptCacheScopeAccount) {
		out.PromptCacheScope = PromptCacheScopeAccount
	}
	switch v := strings.ToLower(strings.TrimSpace(s.SchedulingMode)); v {
	case SchedulingRoundRobin, SchedulingPriority:
		out.SchedulingMode = v
	}
	if s.PromptCacheMaxEntries > 0 {
		out.PromptCacheMaxEntries = s.PromptCacheMaxEntries
	}
	return out
}

// GetCompatConfig returns the compatibility settings with defaults applied.
// It is safe to call before the config file is loaded.
func GetCompatConfig() CompatConfig {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return ResolveCompatSettings(CompatSettings{})
	}
	return ResolveCompatSettings(cfg.Compat)
}

// GetCompatSettings returns the stored settings without defaults, for the admin UI.
func GetCompatSettings() CompatSettings {
	cfgLock.RLock()
	defer cfgLock.RUnlock()
	if cfg == nil {
		return CompatSettings{}
	}
	return cfg.Compat
}

// ValidateCompatSettings rejects values the resolver would silently ignore.
func ValidateCompatSettings(s CompatSettings) error {
	if s.AccountThrottleCooldownMinutes < 0 {
		return fmt.Errorf("accountThrottleCooldownMinutes must not be negative")
	}
	if s.PromptCacheMaxEntries < 0 {
		return fmt.Errorf("promptCacheMaxEntries must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(s.BuilderIDPlaceholderArn)) {
	case "", PlaceholderArnOff, PlaceholderArnOnDemand, PlaceholderArnAlways:
	default:
		return fmt.Errorf("builderIdPlaceholderArn must be off, on_demand, or always")
	}
	switch strings.ToLower(strings.TrimSpace(s.PromptCacheMode)) {
	case "", PromptCacheModeFull, PromptCacheModeReadOnly, PromptCacheModeOff:
	default:
		return fmt.Errorf("promptCacheMode must be full, read_only, or off")
	}
	switch strings.ToLower(strings.TrimSpace(s.PromptCacheScope)) {
	case "", PromptCacheScopeAuto, PromptCacheScopeAccount:
	default:
		return fmt.Errorf("promptCacheScope must be auto or account")
	}
	return nil
}

// UpdateCompatSettings validates and persists new settings.
func UpdateCompatSettings(s CompatSettings) error {
	if err := ValidateCompatSettings(s); err != nil {
		return err
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	cfg.Compat = s
	return Save()
}
