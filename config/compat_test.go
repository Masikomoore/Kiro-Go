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
	if !c.SimulateAnthropicCache || c.SimulateOpenAICache {
		t.Fatalf("cache switches = anthropic %v openai %v", c.SimulateAnthropicCache, c.SimulateOpenAICache)
	}
	off := false
	on := true
	c = ResolveCompatSettings(CompatSettings{SimulateAnthropicCache: &off, SimulateOpenAICache: &on})
	if c.SimulateAnthropicCache || !c.SimulateOpenAICache {
		t.Fatalf("cache switch overrides = anthropic %v openai %v", c.SimulateAnthropicCache, c.SimulateOpenAICache)
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
