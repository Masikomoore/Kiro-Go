package proxy

import (
	"kiro-go/config"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDescribeKiroToolAddsHintOnce(t *testing.T) {
	got := describeKiroTool("Write", "Writes a file.")
	if !strings.HasPrefix(got, "Writes a file.\n") || !strings.HasSuffix(got, writeToolChunkHint) {
		t.Fatalf("unexpected description %q", got)
	}
	if again := describeKiroTool("Write", got); again != got {
		t.Fatalf("hint must not be added twice:\n%q", again)
	}
	if got := describeKiroTool("edit_file", ""); got != editToolChunkHint {
		t.Fatalf("empty description must become the hint, got %q", got)
	}
	if got := describeKiroTool("Read", "Reads a file."); got != "Reads a file." {
		t.Fatalf("other tools must pass through, got %q", got)
	}
}

func TestDescribeKiroToolKeepsHintWhenTruncating(t *testing.T) {
	long := strings.Repeat("规则", maxToolDescLen)
	got := describeKiroTool("Bash", long)
	if len(got) > maxToolDescLen {
		t.Fatalf("description is %d bytes, limit %d", len(got), maxToolDescLen)
	}
	if !strings.HasSuffix(got, bashToolChunkHint) {
		t.Fatal("hint must survive truncation of a long description")
	}
	if !strings.Contains(got, toolDescriptionTruncationMarker) || !utf8.ValidString(got) {
		t.Fatal("truncated description must be marked and valid UTF-8")
	}
}

func TestClaudeToKiroAddsChunkingPolicyForFileTools(t *testing.T) {
	req := &ClaudeRequest{
		Model:     "claude-sonnet-4.5",
		MaxTokens: 1000,
		System:    "base",
		Tools: []ClaudeTool{
			{Name: "Write", Description: "Writes a file.", InputSchema: map[string]interface{}{"type": "object"}},
		},
		Messages: []ClaudeMessage{{Role: "user", Content: "hi"}},
	}

	payload := ClaudeToKiro(req, false)

	priming := payload.ConversationState.History[0].UserInputMessage.Content
	if !strings.HasSuffix(priming, systemChunkingPolicy) {
		t.Fatalf("system prompt must end with the chunking policy, got %q", priming)
	}
	tools := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if !strings.Contains(tools[0].ToolSpecification.Description, writeToolChunkHint) {
		t.Fatalf("Write tool description must carry the hint, got %q", tools[0].ToolSpecification.Description)
	}

	req.Tools[0].Name = "Read"
	priming = ClaudeToKiro(req, false).ConversationState.History[0].UserInputMessage.Content
	if strings.Contains(priming, systemChunkingPolicy) {
		t.Fatal("policy must not be added without a file or shell tool")
	}
}

func TestToolChunkingHintsSwitchOff(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	off := false
	if err := config.UpdateCompatSettings(config.CompatSettings{ToolChunkingHints: &off}); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })

	if got := describeKiroTool("Write", "Writes a file."); got != "Writes a file." {
		t.Fatalf("switch off must leave the description alone, got %q", got)
	}
	if got := appendChunkingPolicy("base", []string{"Write"}); got != "base" {
		t.Fatalf("switch off must leave the system prompt alone, got %q", got)
	}
}
