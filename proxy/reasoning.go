// Native reasoning effort for Kiro models that accept
// additionalModelRequestFields.output_config.effort.
//
// Portions adapted from hank9999/kiro.rs (MIT): the per-model effort tables,
// the budget-to-effort mapping, and the IDE "thinking" wrapper follow
// src/anthropic/converter.rs and src/kiro/endpoint/ide.rs. Kiro CLI 2.6.0 sends
// the bare {"output_config":{"effort":...}} form; Kiro IDE adds the wrapper.
package proxy

import (
	"regexp"
	"strconv"
	"strings"

	"kiro-go/config"
)

const defaultThinkingEffort = "high"

var (
	effortsWithXHigh    = []string{"low", "medium", "high", "xhigh", "max"}
	effortsWithoutXHigh = []string{"low", "medium", "high", "max"}

	kiroModelVersionPattern = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-(\d+)(?:\.(\d+))?`)
)

// ClaudeOutputConfig is the Anthropic output_config request field.
type ClaudeOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// nativeReasoningEfforts returns the effort levels a Kiro model accepts through
// output_config, or nil when it still relies on <thinking_mode> prompt tags.
func nativeReasoningEfforts(modelID string) []string {
	m := kiroModelVersionPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(modelID)))
	if m == nil {
		return nil
	}
	family := m[1]
	major, _ := strconv.Atoi(m[2])
	minor := 0
	if m[3] != "" {
		minor, _ = strconv.Atoi(m[3])
	}
	switch {
	case family == "fable" || family == "mythos" || major >= 5:
		return effortsWithXHigh
	case major == 4 && minor >= 7:
		return effortsWithXHigh
	case major == 4 && minor == 6 && family != "haiku":
		return effortsWithoutXHigh
	default:
		return nil
	}
}

func normalizeThinkingEffort(effort string) string {
	switch e := strings.ToLower(strings.TrimSpace(effort)); e {
	case "low", "medium", "high", "xhigh", "max":
		return e
	case "minimal":
		return "low"
	default:
		return ""
	}
}

func effortFromBudgetTokens(tokens int) string {
	switch {
	case tokens <= 4000:
		return "low"
	case tokens <= 16000:
		return "medium"
	case tokens <= 64000:
		return "high"
	default:
		return "xhigh"
	}
}

// selectReasoningEffort picks the effort to send: an explicit effort first,
// then one derived from an enabled budget, otherwise high. A level the model
// does not support falls back to its strongest one.
func selectReasoningEffort(supported []string, explicit string, thinkingCfg *ClaudeThinkingConfig) string {
	effort := normalizeThinkingEffort(explicit)
	if effort == "" && thinkingCfg != nil && strings.EqualFold(thinkingCfg.Type, "enabled") && thinkingCfg.BudgetTokens > 0 {
		effort = effortFromBudgetTokens(thinkingCfg.BudgetTokens)
	}
	if effort == "" {
		effort = defaultThinkingEffort
	}
	for _, s := range supported {
		if s == effort {
			return effort
		}
	}
	return supported[len(supported)-1]
}

// buildNativeReasoningFields returns additionalModelRequestFields for a model
// that takes output_config.effort, or nil when the model uses prompt tags
// instead or the feature is turned off. A request that did not ask for
// reasoning gets effort=low (compat.lowEffortWithoutThinking): Kiro accepts no
// way to turn thinking off and bills it, and low cost about a quarter less in
// a CA2 probe. With that switch off it gets nil, so Kiro picks its default.
func buildNativeReasoningFields(modelID string, thinking bool, thinkingCfg *ClaudeThinkingConfig, explicitEffort string) map[string]interface{} {
	compat := config.GetCompatConfig()
	if !compat.NativeReasoningEffort {
		return nil
	}
	supported := nativeReasoningEfforts(modelID)
	if supported == nil {
		return nil
	}
	disabled := thinkingCfg != nil && strings.EqualFold(strings.TrimSpace(thinkingCfg.Type), "disabled")
	if normalizeThinkingEffort(explicitEffort) == "" && (disabled || !thinking) {
		if !compat.LowEffortWithoutThinking {
			return nil
		}
		return map[string]interface{}{"output_config": map[string]interface{}{"effort": "low"}}
	}
	return map[string]interface{}{
		"output_config": map[string]interface{}{
			"effort": selectReasoningEffort(supported, explicitEffort, thinkingCfg),
		},
	}
}

// additionalFieldsForEndpoint adds the IDE "thinking" wrapper to an
// output_config request. The CLI (API key) protocol takes the bare form. The
// payload's own map is never mutated, so endpoint fallback can reuse it.
func additionalFieldsForEndpoint(fields map[string]interface{}, isAPIKey bool) map[string]interface{} {
	if len(fields) == 0 || isAPIKey {
		return fields
	}
	if _, ok := fields["output_config"]; !ok {
		return fields
	}
	if _, ok := fields["thinking"]; ok {
		return fields
	}
	out := make(map[string]interface{}, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	out["thinking"] = map[string]interface{}{"type": "adaptive", "display": "summarized"}
	return out
}

// thinkingModePromptFor returns the prompt-tag form used by models without
// native reasoning fields, honoring an explicit enabled budget.
func thinkingModePromptFor(thinkingCfg *ClaudeThinkingConfig) string {
	if thinkingCfg != nil && strings.EqualFold(thinkingCfg.Type, "enabled") && thinkingCfg.BudgetTokens > 0 {
		return "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>" + strconv.Itoa(thinkingCfg.BudgetTokens) + "</max_thinking_length>"
	}
	return ThinkingModePrompt
}
