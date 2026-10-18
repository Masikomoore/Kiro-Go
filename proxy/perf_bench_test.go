package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// buildBenchClaudeRequest returns a Claude Code style request body: a long
// system prompt, many tools, and a long tool-heavy history (~1MB).
func buildBenchClaudeRequest(b *testing.B, turns int) []byte {
	b.Helper()
	tools := make([]map[string]interface{}, 0, 40)
	for i := 0; i < 40; i++ {
		tools = append(tools, map[string]interface{}{
			"name":        fmt.Sprintf("Tool%d", i),
			"description": strings.Repeat("Describes what the tool does. ", 40),
			"input_schema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string", "description": strings.Repeat("p", 200)},
					"content": map[string]interface{}{"type": "string"},
				},
				"required": []string{"path"},
			},
		})
	}
	messages := []map[string]interface{}{{"role": "user", "content": "Start the task."}}
	for i := 0; i < turns; i++ {
		id := fmt.Sprintf("toolu_%04d", i)
		messages = append(messages,
			map[string]interface{}{"role": "assistant", "content": []map[string]interface{}{
				{"type": "text", "text": strings.Repeat("Reading the next file. ", 10)},
				{"type": "tool_use", "id": id, "name": "Tool1", "input": map[string]interface{}{"path": fmt.Sprintf("/src/file%d.go", i)}},
			}},
			map[string]interface{}{"role": "user", "content": []map[string]interface{}{
				{"type": "tool_result", "tool_use_id": id, "content": strings.Repeat("func example() { return 42 }\n", 200)},
			}},
		)
	}
	last := messages[len(messages)-1]["content"].([]map[string]interface{})
	last[0]["cache_control"] = map[string]interface{}{"type": "ephemeral"}
	body, err := json.Marshal(map[string]interface{}{
		"model":      "claude-sonnet-4.6",
		"max_tokens": 32000,
		"stream":     true,
		"system": []map[string]interface{}{
			{"type": "text", "text": strings.Repeat("You are Claude Code. Follow the rules. ", 500), "cache_control": map[string]interface{}{"type": "ephemeral"}},
		},
		"tools":    tools,
		"messages": messages,
		"thinking": map[string]interface{}{"type": "adaptive"},
	})
	if err != nil {
		b.Fatal(err)
	}
	return body
}

// BenchmarkClaudePreflight measures the CPU work between reading the client
// body and sending the upstream request, including the payload marshals done
// in callKiroAPIContextOnce.
func BenchmarkClaudePreflight(b *testing.B) {
	body := buildBenchClaudeRequest(b, 150)
	tracker := newPromptCacheTracker(time.Hour)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var req ClaudeRequest
		if err := json.Unmarshal(body, &req); err != nil {
			b.Fatal(err)
		}
		effective := cloneClaudeRequestForThinking(&req, true)
		est := estimateClaudeRequestInputTokens(effective)
		tracker.BuildClaudeProfile(effective, est)
		payload := ClaudeToKiro(&req, true)
		for j := 0; j < kiroPayloadMarshalsPerCall; j++ {
			if _, err := json.Marshal(payload); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// kiroPayloadMarshalsPerCall is how many times callKiroAPIContextOnce encodes
// the full payload per endpoint with DEBUG off. It was 3 before the debug dump
// was gated and the validation-only encode was dropped.
const kiroPayloadMarshalsPerCall = 1
