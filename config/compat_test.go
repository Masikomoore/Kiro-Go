package config

import (
	"testing"
	"time"
)

func TestResolveCompatSettingsDefaults(t *testing.T) {
	c := ResolveCompatSettings(CompatSettings{})
	if !c.NativeReasoningEffort || !c.ToolChunkingHints || !c.ThinkingSignature || !c.ToolUseXMLFilter || !c.ImageDedup {
		t.Fatalf("default-on switches must be on: %+v", c)
	}
	if c.StructuredToolHistory || c.NormalizeKiroToolParams || c.ImageDownscale {
		t.Fatalf("experimental switches must be off: %+v", c)
	}
	if c.AccountThrottleCooldown != 10*time.Minute || c.BuilderIDPlaceholderArn != PlaceholderArnOff ||
		c.PromptCacheMode != PromptCacheModeFull || c.PromptCacheScope != PromptCacheScopeAuto || c.PromptCacheMaxEntries != 20000 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if !c.SimulateAnthropicCache || !c.ImplicitAnthropicCache {
		t.Fatalf("cache switches = anthropic %v implicit %v", c.SimulateAnthropicCache, c.ImplicitAnthropicCache)
	}
	off := false
	c = ResolveCompatSettings(CompatSettings{SimulateAnthropicCache: &off, ImplicitAnthropicCache: &off})
	if c.SimulateAnthropicCache || c.ImplicitAnthropicCache {
		t.Fatalf("cache switch overrides = anthropic %v implicit %v", c.SimulateAnthropicCache, c.ImplicitAnthropicCache)
	}

	c = ResolveCompatSettings(CompatSettings{ToolChunkingHints: &off, AccountThrottleCooldownMinutes: 3, PromptCacheMode: "READ_ONLY"})
	if c.ToolChunkingHints || c.AccountThrottleCooldown != 3*time.Minute || c.PromptCacheMode != PromptCacheModeReadOnly {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestValidateCompatSettings(t *testing.T) {
	bad := []CompatSettings{
		{AccountThrottleCooldownMinutes: -1},
		{PromptCacheMaxEntries: -5},
		{BuilderIDPlaceholderArn: "sometimes"},
		{PromptCacheMode: "maybe"},
		{PromptCacheScope: "global"},
	}
	for _, s := range bad {
		if ValidateCompatSettings(s) == nil {
			t.Errorf("expected %+v to be rejected", s)
		}
	}
	if err := ValidateCompatSettings(CompatSettings{BuilderIDPlaceholderArn: "on_demand", PromptCacheScope: "account"}); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
}

func TestMaxPayloadKB(t *testing.T) {
	if got := ResolveCompatSettings(CompatSettings{}).MaxPayloadBytes; got != 2048*1024 {
		t.Fatalf("default = %d, want 2 MB", got)
	}
	if got := ResolveCompatSettings(CompatSettings{MaxPayloadKB: 900}).MaxPayloadBytes; got != 900*1024 {
		t.Fatalf("900 KB = %d", got)
	}
	for _, kb := range []int{-1, 100, 9000} {
		if err := ValidateCompatSettings(CompatSettings{MaxPayloadKB: kb}); err == nil {
			t.Fatalf("maxPayloadKB=%d must be rejected", kb)
		}
	}
	if !ResolveCompatSettings(CompatSettings{}).CapMaxEffort {
		t.Fatal("capMaxEffort must default on")
	}
}

func TestProxyFailoverSettingsDefaultsAndRanges(t *testing.T) {
	def := ResolveCompatSettings(CompatSettings{})
	if def.UpstreamConnectTimeout != 15*time.Second || def.ProxyDownCooldown != 2*time.Minute {
		t.Fatalf("defaults = connect %s, proxy down %s; want 15s and 2m", def.UpstreamConnectTimeout, def.ProxyDownCooldown)
	}
	set := ResolveCompatSettings(CompatSettings{UpstreamConnectTimeoutSeconds: 20, ProxyDownSeconds: 300})
	if set.UpstreamConnectTimeout != 20*time.Second || set.ProxyDownCooldown != 5*time.Minute {
		t.Fatalf("configured = connect %s, proxy down %s", set.UpstreamConnectTimeout, set.ProxyDownCooldown)
	}

	valid := []CompatSettings{{}, {UpstreamConnectTimeoutSeconds: 5}, {UpstreamConnectTimeoutSeconds: 60}, {ProxyDownSeconds: 30}, {ProxyDownSeconds: 1800}}
	for _, s := range valid {
		if err := ValidateCompatSettings(s); err != nil {
			t.Errorf("ValidateCompatSettings(%+v) = %v, want nil", s, err)
		}
	}
	invalid := []CompatSettings{{UpstreamConnectTimeoutSeconds: 4}, {UpstreamConnectTimeoutSeconds: 61}, {ProxyDownSeconds: 29}, {ProxyDownSeconds: 1801}}
	for _, s := range invalid {
		if err := ValidateCompatSettings(s); err == nil {
			t.Errorf("ValidateCompatSettings(%+v) = nil, want an error", s)
		}
	}
}
