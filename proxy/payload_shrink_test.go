package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"kiro-go/config"
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

// useImageCap lowers maxPayloadImageBytes for one test, so image fixtures of
// a few hundred KB exercise the image cut.
func useImageCap(t *testing.T, bytes int) {
	t.Helper()
	old := maxPayloadImageBytes
	maxPayloadImageBytes = bytes
	t.Cleanup(func() { maxPayloadImageBytes = old })
}

// Kiro's input limit counts text, not image data (CA2, 2026-10-27: 3.2–5.6 MB
// of history images passed). Images under their own cap are sent whole even
// when the payload is far over maxPayloadBytes().
func TestTruncatePayloadKeepsImagesUnderImageCap(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900})
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i := 0; i < 16; i++ {
		history = append(history, userTurn(fmt.Sprintf("turn %d", i), kiroImage(fakePNG(i, 90*1024))), assistantTurn("ok"))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "now", ModelID: "claude-sonnet-4.5"}

	truncatePayloadToLimit(payload, true)

	if payloadByteSize(payload) <= maxPayloadBytes() {
		t.Fatal("fixture must be over the byte limit")
	}
	if payload.Shrink.Stage != "" {
		t.Fatalf("stage = %q, want nothing shrunk", payload.Shrink.Stage)
	}
	for i, h := range payload.ConversationState.History[2:] {
		if u := h.UserInputMessage; u != nil && len(u.Images) != 1 {
			t.Fatalf("history entry %d lost its image", i+2)
		}
	}
}

// Text over the limit with images under their cap: text is compacted or turns
// dropped, but the kept turns keep their images.
func TestTruncatePayloadTextOverLimitKeepsImages(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900})
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i := 0; i < 12; i++ {
		history = append(history, userTurn(fmt.Sprintf("result %d: %s", i, strings.Repeat("x", 100*1024)), kiroImage(fakePNG(i, 90*1024))), assistantTurn("ok"))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "now", ModelID: "claude-sonnet-4.5"}

	truncatePayloadToLimit(payload, true)

	if payload.Shrink.Stage != "compact" {
		t.Fatalf("stage = %q, want compact", payload.Shrink.Stage)
	}
	if payloadTextBytes(payload) > maxPayloadBytes() {
		t.Fatalf("text still %d bytes", payloadTextBytes(payload))
	}
	for i, h := range payload.ConversationState.History[2:] {
		if u := h.UserInputMessage; u != nil && len(u.Images) != 1 {
			t.Fatalf("history entry %d lost its image", i+2)
		}
	}
}

func TestTruncatePayloadDropsOldImagesBeforeTurns(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	useImageCap(t, 900*1024)
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i := 0; i < 8; i++ {
		history = append(history, userTurn(fmt.Sprintf("turn %d", i), kiroImage(fakePNG(i, 90*1024))), assistantTurn("ok"))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "now", ModelID: "claude-sonnet-4.5"}

	truncatePayloadToLimit(payload, true)

	if size := payloadImageBytes(payload); size > maxPayloadImageBytes {
		t.Fatalf("images still %d bytes", size)
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
	if max := historyImageMinKeepUserTurns + historyImageWindowStep - 1; withImages < historyImageMinKeepUserTurns || withImages > max {
		t.Fatalf("expected images on the latest %d–%d history user turns, got %d", historyImageMinKeepUserTurns, max, withImages)
	}
}

func TestTruncatePayloadCompactsLargeHistoryTextBeforeDroppingTurns(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
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
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: strings.Repeat("字", maxPayloadBytes())}
	truncateCurrentMessage(payload)
	if !utf8.ValidString(payload.ConversationState.CurrentMessage.UserInputMessage.Content) {
		t.Fatal("hard truncation must not split a rune")
	}
}

// A current message whose images alone exceed the byte limit used to drop
// history and blank the user's text (live: 82 of 86 entries dropped, 1.6 MB
// still sent). Image data no longer counts toward that limit, so nothing is
// shrunk at all.
func TestTruncatePayloadKeepsHistoryWhenCurrentImagesAloneExceedLimit(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i := 0; i < 20; i++ {
		history = append(history, userTurn(fmt.Sprintf("turn %d: %s", i, strings.Repeat("y", 20*1024))), assistantTurn("ok"))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "what is in this screenshot?", ModelID: "claude-sonnet-4.5",
		Images: []KiroImage{kiroImage(fakePNG(1, 800*1024))},
	}

	truncatePayloadToLimit(payload, true)

	if got := len(payload.ConversationState.History); got != len(history) {
		t.Fatalf("history must be kept when dropping it cannot help: %d -> %d entries", len(history), got)
	}
	if got := payload.ConversationState.CurrentMessage.UserInputMessage.Content; got != "what is in this screenshot?" {
		t.Fatalf("current text must be kept, got %q", got)
	}
	if payload.Shrink.Stage != "" || payload.Shrink.DroppedTurns != 0 {
		t.Fatalf("shrink = %+v, want nothing shrunk", payload.Shrink)
	}
}

// A current message whose text is what overflows is still truncated.
func TestTruncatePayloadStillTruncatesOversizedCurrentText(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	payload := &KiroPayload{}
	payload.ConversationState.History = []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: strings.Repeat("z", 1200*1024), ModelID: "claude-sonnet-4.5"}

	truncatePayloadToLimit(payload, true)

	if size := payloadByteSize(payload); size > maxPayloadBytes() {
		t.Fatalf("payload still %d bytes", size)
	}
	if payload.Shrink.Stage != "current" {
		t.Fatalf("stage = %q, want current", payload.Shrink.Stage)
	}
}

// The default limit is 2 MB (compat.maxPayloadKB): a 1.5 MB payload that the
// old 900 KB limit truncated is now sent whole (live: Kiro accepted 1.06–1.6 MB).
func TestTruncatePayloadDefaultLimitKeepsLargeHistory(t *testing.T) {
	build := func() *KiroPayload {
		payload := &KiroPayload{}
		history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
		for i := 0; i < 60; i++ {
			history = append(history, userTurn(fmt.Sprintf("turn %d: %s", i, strings.Repeat("w", 25*1024))), assistantTurn("ok"))
		}
		payload.ConversationState.History = history
		payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{Content: "now", ModelID: "claude-sonnet-4.5"}
		return payload
	}
	useCompat(t, config.CompatSettings{})
	p := build()
	before := len(p.ConversationState.History)
	truncatePayloadToLimit(p, true)
	if p.Shrink.Stage != "" || len(p.ConversationState.History) != before {
		t.Fatalf("a %d-byte payload must fit the 2 MB default, got stage %q", payloadByteSize(p), p.Shrink.Stage)
	}

	useCompat(t, config.CompatSettings{MaxPayloadKB: 900})
	p = build()
	truncatePayloadToLimit(p, true)
	if p.Shrink.Stage == "" || payloadByteSize(p) > 900*1024 {
		t.Fatalf("with maxPayloadKB=900 the payload must be shrunk to fit, got stage %q size %d", p.Shrink.Stage, payloadByteSize(p))
	}
}

// imageTurns returns one 90 KB image per user turn; repeat maps a turn to the
// earlier turn whose image it attaches again.
func imageTurns(n int, repeat map[int]int) []int {
	seeds := make([]int, n)
	for i := range seeds {
		seeds[i] = i
		if from, ok := repeat[i]; ok {
			seeds[i] = from
		}
	}
	return seeds
}

// sentHistory is the Kiro history sent for a conversation whose user turns
// carry the given image seeds, the last one being the current message.
func sentHistory(seeds []int) []string {
	payload := &KiroPayload{}
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i, s := range seeds[:len(seeds)-1] {
		history = append(history, userTurn(fmt.Sprintf("turn %d", i), kiroImage(fakePNG(s, 90*1024))), assistantTurn(fmt.Sprintf("ok %d", i)))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "now", ModelID: "claude-sonnet-4.5", Images: []KiroImage{kiroImage(fakePNG(seeds[len(seeds)-1], 90*1024))},
	}
	dedupeKiroImages(payload)
	truncatePayloadToLimit(payload, true)
	out := make([]string, len(payload.ConversationState.History))
	for i, h := range payload.ConversationState.History {
		raw, _ := json.Marshal(h)
		out[i] = string(raw)
	}
	return out
}

// sentForConversation is like sentHistory with a conversation ID, so the
// remembered image cut applies, and images of imageKB KB. It also returns the
// image data size sent.
func sentForConversation(conv string, seeds []int, imageKB int) ([]string, int) {
	payload := &KiroPayload{}
	payload.ConversationState.ConversationID = conv
	history := []KiroHistoryMessage{userTurn("system"), assistantTurn("I will follow these instructions.")}
	for i, s := range seeds[:len(seeds)-1] {
		history = append(history, userTurn(fmt.Sprintf("turn %d", i), kiroImage(fakePNG(s, imageKB*1024))), assistantTurn(fmt.Sprintf("ok %d", i)))
	}
	payload.ConversationState.History = history
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "now", ModelID: "claude-sonnet-4.5", Images: []KiroImage{kiroImage(fakePNG(seeds[len(seeds)-1], imageKB*1024))},
	}
	dedupeKiroImages(payload)
	truncatePayloadToLimit(payload, true)
	out := make([]string, len(payload.ConversationState.History))
	for i, h := range payload.ConversationState.History {
		raw, _ := json.Marshal(h)
		out[i] = string(raw)
	}
	return out, payloadImageBytes(payload)
}

func resetImageCuts(t *testing.T) {
	t.Helper()
	reset := func() {
		imageCutsMu.Lock()
		imageCuts = make(map[string]imageCut)
		imageCutsMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// CA2 conv c025ebb1 (2026-10-27): the fixed window cut every 3 user turns
// once over the limit; 3 of 3 cuts were billed cold. With a conversation ID
// the cut now stays put while the images fit their cap, so over 40 turns of
// one new image each it moves far less often.
func TestImageCutMovesRarelyWithConversationID(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900})
	useImageCap(t, 900*1024)
	resetImageCuts(t)
	seeds := imageTurns(60, nil)
	moves, overLimit := 0, 0
	var prev []string
	for n := 20; n <= len(seeds); n++ {
		cur, size := sentForConversation("conv-a", seeds[:n], 40)
		if size > maxPayloadImageBytes {
			overLimit++
		}
		if prev != nil && firstChange(prev, cur) >= 0 {
			moves++
		}
		prev = cur
	}
	if overLimit > 0 {
		t.Fatalf("%d payloads over the limit", overLimit)
	}
	// 40 KB a turn: the fixed window moves every 3 turns (~13 times); the
	// remembered cut refills from half the limit, about every 11 turns.
	if moves > 5 {
		t.Fatalf("image cut moved %d times in 40 turns", moves)
	}
}

// firstChange is the first index where an entry sent before differs now, or -1.
func firstChange(prev, cur []string) int {
	for i := 0; i < len(prev) && i < len(cur); i++ {
		if prev[i] != cur[i] {
			return i
		}
	}
	return -1
}

// Repro of conv 243fc8a2 (CA2, 2026-10-24): Kiro history entry 81 changed in
// two turns while the payload was over the limit. The first change is the
// 5-user-turn image window reaching it; the second came when a later turn
// attached one of its images again, and dedup rewrote the entry's note. Each
// rewrite makes Kiro write everything after it to its cache again.
func TestShrinkDoesNotRewriteAgedEntryWhenImageIsAttachedAgain(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	useImageCap(t, 900*1024)
	seeds := imageTurns(14, map[int]int{12: 3})
	prev := sentHistory(seeds[:12])
	cur := sentHistory(seeds[:13])
	if i := firstChange(prev, cur); i >= 0 && i < len(cur)-10 {
		t.Fatalf("turn 3 (entry 8) already lost its image; re-attaching it rewrote entry %d of %d", i, len(cur))
	}
}

// Repro: once over the limit, every new turn moves the image window by one
// user turn and rewrites the entry ten back, so Kiro writes ~10 entries again
// every turn (243fc8a2: 16k–34k tokens of cache creation per turn).
func TestShrinkDoesNotRewriteHistoryEveryTurn(t *testing.T) {
	useCompat(t, config.CompatSettings{MaxPayloadKB: 900}) // fixtures are sized for a 900 KB limit
	useImageCap(t, 900*1024)
	seeds := imageTurns(20, nil)
	rewrites := 0
	prev := sentHistory(seeds[:10])
	for n := 11; n <= len(seeds); n++ {
		cur := sentHistory(seeds[:n])
		if firstChange(prev, cur) >= 0 {
			rewrites++
		}
		prev = cur
	}
	if rewrites > 10/historyImageWindowStep+1 {
		t.Fatalf("history rewritten in %d of 10 turns over the limit", rewrites)
	}
}
