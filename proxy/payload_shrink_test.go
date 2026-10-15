package proxy

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// fakePNG returns a distinct PNG-signed base64 blob of roughly size bytes.
func fakePNG(seed, size int) string {
	raw := make([]byte, size)
	copy(raw, "\x89PNG\r\n\x1a\n")
	for i := 8; i < size; i++ {
		raw[i] = byte((i*31 + seed*7) % 251)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func kiroImage(data string) KiroImage {
	img := KiroImage{Format: "png"}
	img.Source.Bytes = data
	return img
}

func userTurn(content string, images ...KiroImage) KiroHistoryMessage {
	return KiroHistoryMessage{UserInputMessage: &KiroUserInputMessage{Content: content, ModelID: "claude-sonnet-4.5", Origin: "AI_EDITOR", Images: images}}
}

func assistantTurn(content string) KiroHistoryMessage {
	return KiroHistoryMessage{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: content}}
}

func TestParseBase64ImageCorrectsMislabeledFormat(t *testing.T) {
	img := parseBase64Image(onePixelPNG, "jpeg")
	if img == nil || img.Format != "png" {
		t.Fatalf("PNG bytes labelled jpeg must be sent as png, got %+v", img)
	}
	if parseBase64Image("not base64!!", "png") != nil {
		t.Fatal("invalid base64 must be rejected")
	}
}

func TestDedupeKiroImagesKeepsLatestCopy(t *testing.T) {
	payload := &KiroPayload{}
	payload.ConversationState.History = []KiroHistoryMessage{
		userTurn("first look", kiroImage(onePixelPNG)),
		assistantTurn("seen"),
	}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "again", Images: []KiroImage{kiroImage(onePixelPNG), kiroImage(fakePNG(1, 64))},
	}

	dedupeKiroImages(payload)

	if got := len(payload.ConversationState.CurrentMessage.UserInputMessage.Images); got != 2 {
		t.Fatalf("current message must keep its images, got %d", got)
	}
	old := payload.ConversationState.History[0].UserInputMessage
	if len(old.Images) != 0 || !strings.Contains(old.Content, duplicateImagePlaceholder) {
		t.Fatalf("earlier duplicate must be replaced by a note, got %+v", old)
	}
}

func TestTruncatePayloadDropsOldImagesBeforeTurns(t *testing.T) {
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i := 0; i < 8; i++ {
		history = append(history, userTurn(fmt.Sprintf("turn %d", i), kiroImage(fakePNG(i, 90*1024))), assistantTurn("ok"))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "now", ModelID: "claude-sonnet-4.5"}

	truncatePayloadToLimit(payload, true)

	if size := payloadByteSize(payload); size > maxPayloadBytes {
		t.Fatalf("payload still %d bytes", size)
	}
	got := payload.ConversationState.History
	if len(got) != len(history) {
		t.Fatalf("turns must be kept when dropping old images is enough: %d -> %d", len(history), len(got))
	}
	withImages := 0
	for _, h := range got {
		if h.UserInputMessage != nil && len(h.UserInputMessage.Images) > 0 {
			withImages++
		}
		if h.UserInputMessage != nil && h.UserInputMessage.Content == truncationPlaceholder {
			t.Fatal("no turn should have been dropped")
		}
	}
	if withImages != historyImageKeepUserTurns-1 {
		t.Fatalf("expected images on the latest %d history user turns, got %d", historyImageKeepUserTurns-1, withImages)
	}
}

func TestTruncatePayloadCompactsLargeHistoryTextBeforeDroppingTurns(t *testing.T) {
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i := 0; i < 12; i++ {
		history = append(history, userTurn(fmt.Sprintf("result %d: %s", i, strings.Repeat("x", 100*1024))), assistantTurn("ok"))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "now", ModelID: "claude-sonnet-4.5"}

	truncatePayloadToLimit(payload, true)

	got := payload.ConversationState.History
	if len(got) != len(history) {
		t.Fatalf("compaction alone should fit, turns %d -> %d", len(history), len(got))
	}
	if !strings.Contains(got[2].UserInputMessage.Content, "[Content truncated to fit the upstream input limit") {
		t.Fatalf("large history text must be compacted, got %.80q", got[2].UserInputMessage.Content)
	}
	if got[0].UserInputMessage.Content != "system" {
		t.Fatal("system priming must not be compacted")
	}
}

func TestCapToolResultTexts(t *testing.T) {
	big := "HEAD" + strings.Repeat("中", 200*1024) + "TAIL"
	results := []KiroToolResult{{ToolUseID: "t1", Content: []KiroResultContent{{Text: big}}}, {ToolUseID: "t2", Content: []KiroResultContent{{Text: "small"}}}}

	capToolResultTexts(results)

	got := results[0].Content[0].Text
	if len(got) > currentToolResultMaxBytes+200 {
		t.Fatalf("tool result is %d bytes after capping", len(got))
	}
	if !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") || !utf8.ValidString(got) {
		t.Fatal("capped text must keep head and tail and stay valid UTF-8")
	}
	if results[1].Content[0].Text != "small" {
		t.Fatal("small results must pass through")
	}
}

func TestTruncateCurrentMessageKeepsValidUTF8(t *testing.T) {
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: strings.Repeat("字", maxPayloadBytes)}
	truncateCurrentMessage(payload)
	if !utf8.ValidString(payload.ConversationState.CurrentMessage.UserInputMessage.Content) {
		t.Fatal("hard truncation must not split a rune")
	}
}
