// Chunked-write hints for file and shell tools.
//
// Kiro can cut one assistant response off before a large tool argument is
// complete, which surfaces as errIncompleteKiroToolInput; retrying the same
// oversized Write fails the same way. Telling the model to chunk large
// payloads avoids the failure instead of retrying it.
//
// Portions adapted from hank9999/kiro.rs (MIT): the hint text follows
// src/anthropic/converter.rs.
package proxy

import (
	"strings"

	"kiro-go/config"
)

const (
	writeToolChunkHint = "- IMPORTANT: The upstream API can truncate one assistant response before a large tool argument is complete. If the content to write exceeds 150 lines, 8,000 characters, or roughly 4,000 tokens, you MUST NOT write it all at once. Write only a small skeleton or first chunk using this tool (no more than 50 lines and no more than 4,000 characters), leave a unique placeholder if needed, then use `Edit` to append or replace the remaining content in chunks of no more than 50 lines and no more than 4,000 characters each. If a Write/Edit attempt fails, do not retry the same large payload; split it smaller."
	editToolChunkHint  = "- IMPORTANT: The upstream API can truncate one assistant response before a large tool argument is complete. If `old_string` or `new_string` exceeds 50 lines, 8,000 characters, or roughly 4,000 tokens, you MUST split the modification into multiple Edit calls, each replacing no more than 50 lines and no more than 4,000 characters at a time. If used to append content, leave a unique placeholder to help append content. On the final chunk, do NOT include the placeholder. If an Edit attempt fails, do not retry the same large payload; split it smaller."
	bashToolChunkHint  = "- IMPORTANT: Do not send very large commands, inline scripts, or heredocs through Bash. If a command would exceed 100 lines, 8,000 characters, or roughly 4,000 tokens, create or modify a script/file with chunked Write/Edit calls first, then run a short Bash command that executes it. If a Bash attempt fails due to argument size or truncation, do not retry the same large command; split it smaller."

	systemChunkingPolicy = "Single tool arguments can be truncated by the upstream API when a response is too large. Always chunk large Write/Edit/Bash payloads before they approach 8,000 characters, 150 Write lines, 50 Edit lines, or roughly 4,000 tokens. Never retry the same oversized tool payload after a failure. Never bypass these limits with a large Bash heredoc or inline script. Complete all chunked operations without commentary."

	toolDescriptionTruncationMarker = "... (description truncated)"
)

// toolChunkHint returns the hint for a file-writing, file-editing, or shell
// tool, matched case-insensitively across common client naming schemes.
func toolChunkHint(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write", "write_to_file", "fswrite", "fs_write", "create_file":
		return writeToolChunkHint
	case "edit", "multiedit", "edit_file", "str_replace", "str_replace_editor", "apply_diff":
		return editToolChunkHint
	case "bash", "execute_bash", "run_command":
		return bashToolChunkHint
	default:
		return ""
	}
}

// describeKiroTool builds the description sent to Kiro: the client text,
// plus the chunking hint for file and shell tools, within maxToolDescLen. The
// hint survives truncation of a long client description, and is never added
// twice.
func describeKiroTool(name, description string) string {
	hint := ""
	if config.GetCompatConfig().ToolChunkingHints {
		hint = toolChunkHint(name)
	}
	if hint == "" || strings.Contains(description, hint) {
		return limitToolDescription(description)
	}
	base := strings.TrimRight(description, "\n")
	if strings.TrimSpace(base) == "" {
		return hint
	}
	joined := base + "\n" + hint
	if len(joined) <= maxToolDescLen {
		return joined
	}
	room := maxToolDescLen - len(hint) - 1 - len(toolDescriptionTruncationMarker)
	if room <= 0 {
		return limitToolDescription(joined)
	}
	return truncateUTF8Bytes(base, room) + toolDescriptionTruncationMarker + "\n" + hint
}

// appendChunkingPolicy adds the system-level chunking rule when the request
// carries a tool that has a hint. It runs after prompt filters, so a replaced
// Claude Code system prompt still gets it.
func appendChunkingPolicy(systemPrompt string, toolNames []string) string {
	if !config.GetCompatConfig().ToolChunkingHints || strings.Contains(systemPrompt, systemChunkingPolicy) {
		return systemPrompt
	}
	for _, name := range toolNames {
		if toolChunkHint(name) != "" {
			if strings.TrimSpace(systemPrompt) == "" {
				return systemChunkingPolicy
			}
			return systemPrompt + "\n\n" + systemChunkingPolicy
		}
	}
	return systemPrompt
}

func claudeToolNames(tools []ClaudeTool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return names
}

func openAIToolNames(tools []OpenAITool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Function.Name)
	}
	return names
}
