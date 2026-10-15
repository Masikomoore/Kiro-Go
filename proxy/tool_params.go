// Kiro-style tool argument normalization (experimental,
// compat.normalizeKiroToolParams).
//
// The Kiro model is tuned on its own tools (fs_write, str_replace, read_file,
// ...) and sometimes fills a Claude Code tool with those argument names, which
// Claude Code rejects as invalid parameters. This renames such arguments only
// when the Claude Code name is missing. It never replaces the client's tools:
// kiro.rs maps them to Kiro built-ins, which drops options such as Grep's
// output_mode, Edit's replace_all, and Bash's run_in_background.
// Alias table adapted from hank9999/kiro.rs (MIT).
package proxy

import "kiro-go/config"

type toolParamAlias struct {
	want string
	from []string
}

var claudeCodeToolParamAliases = map[string][]toolParamAlias{
	"Write": {{"file_path", []string{"path"}}, {"content", []string{"text"}}},
	"Edit": {
		{"file_path", []string{"path"}},
		{"old_string", []string{"oldStr", "old_str"}},
		{"new_string", []string{"newStr", "new_str"}},
	},
	"Read": {{"file_path", []string{"path"}}},
	"Glob": {{"pattern", []string{"query"}}},
	"Grep": {{"pattern", []string{"query"}}, {"glob", []string{"includePattern"}}},
}

// normalizeToolUseParams renames Kiro-style arguments on a known Claude Code
// tool. Tool names are matched exactly so other clients' tools are untouched.
func normalizeToolUseParams(tu *KiroToolUse) {
	aliases, ok := claudeCodeToolParamAliases[tu.Name]
	if !ok || tu.Input == nil {
		return
	}
	for _, a := range aliases {
		if _, present := tu.Input[a.want]; present {
			continue
		}
		for _, from := range a.from {
			if v, present := tu.Input[from]; present {
				tu.Input[a.want] = v
				delete(tu.Input, from)
				break
			}
		}
	}
	if tu.Name == "Read" {
		normalizeReadLineRange(tu.Input)
	}
}

// normalizeReadLineRange turns read_file's start_line/end_line into Claude
// Code Read's offset/limit.
func normalizeReadLineRange(input map[string]interface{}) {
	_, hasOffset := input["offset"]
	_, hasLimit := input["limit"]
	start, hasStart := toolParamNumber(input["start_line"])
	end, hasEnd := toolParamNumber(input["end_line"])
	if hasStart && !hasOffset {
		input["offset"] = start
	}
	if hasEnd && !hasLimit {
		limit := end
		if hasStart {
			limit = end - start + 1
		}
		if limit > 0 {
			input["limit"] = limit
		}
	}
	if hasStart || hasEnd {
		delete(input, "start_line")
		delete(input, "end_line")
	}
}

func toolParamNumber(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// restoreToolUse maps a Kiro tool name back to the client's name and, when
// enabled, normalizes Kiro-style arguments.
func restoreToolUse(tu KiroToolUse, nameMap map[string]string, normalize bool) KiroToolUse {
	if original, ok := nameMap[tu.Name]; ok {
		tu.Name = original
	}
	if normalize {
		normalizeToolUseParams(&tu)
	}
	return tu
}

func normalizeKiroToolParamsEnabled() bool {
	return config.GetCompatConfig().NormalizeKiroToolParams
}
