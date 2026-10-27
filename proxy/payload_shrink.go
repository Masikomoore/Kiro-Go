// Payload size control applied before the Kiro request is sent.
//
// Kiro rejects oversized input with CONTENT_LENGTH_EXCEEDS_THRESHOLD. Dropping
// whole history turns loses the most context, so cheaper reductions run first:
// duplicate images, images in old turns, then the head and tail of very large
// history text. Ideas from kiro.rs (MIT) and the ikik gateway.
package proxy

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"kiro-go/config"
)

const (
	duplicateImagePlaceholder = "[Image omitted: the same image is attached later in this conversation.]"
	oldHistoryImagesFormat    = "[%d image(s) omitted from older conversation history.]"

	// When the payload is over the limit, images are dropped from all but the
	// newest history user turns. The cut moves in steps of
	// historyImageWindowStep user turns, so between historyImageMinKeepUserTurns
	// and historyImageMinKeepUserTurns+historyImageWindowStep-1 history user
	// turns keep their images. A cut that moved every turn rewrote the entry
	// ten back on every turn (CA2 conv 243fc8a2, 2026-10-24), and Kiro wrote
	// everything after it to its cache again: 16k–34k tokens a turn.
	historyImageMinKeepUserTurns = 2
	historyImageWindowStep       = 3

	// With a conversation ID the cut is remembered instead (imageCuts): it
	// stays put while the images fit maxPayloadImageBytes, and when it must
	// move it goes down to imageCutTargetFraction of that cap. The fixed
	// window above cut every 3 user turns even when one new screenshot pushed
	// the payload over (CA2 conv c025ebb1, 2026-10-27; 3 of 3 late cuts billed
	// cold, as Kiro misses its cache once an image is removed).
	imageCutTargetFraction = 0.5
	imageCutTTL            = 2 * time.Hour
	imageCutMax            = 2048

	// History text above historyTextCompactLimit bytes is cut to its head and
	// tail when the payload is over the limit.
	historyTextCompactLimit = 12000
	historyTextKeepHead     = 4000
	historyTextKeepTail     = 2000

	// currentToolResultMaxBytes caps a single tool result in the current turn.
	// One field near this size already triggers CONTENT_LENGTH_EXCEEDS_THRESHOLD.
	currentToolResultMaxBytes = 400 * 1024
)

// compactText keeps the first head and last tail bytes of s, on rune
// boundaries, with a note saying how much was cut.
func compactText(s string, head, tail int) string {
	if len(s) <= head+tail {
		return s
	}
	h := truncateUTF8Bytes(s, head)
	start := len(s) - tail
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	t := s[start:]
	omitted := utf8.RuneCountInString(s) - utf8.RuneCountInString(h) - utf8.RuneCountInString(t)
	return fmt.Sprintf("%s\n\n[Content truncated to fit the upstream input limit: %d characters omitted.]\n\n%s", h, omitted, t)
}

// capToolResultTexts shrinks any current-turn tool result text above
// currentToolResultMaxBytes to its head and tail.
func capToolResultTexts(results []KiroToolResult) {
	head := currentToolResultMaxBytes * 2 / 3
	tail := currentToolResultMaxBytes / 3
	for i := range results {
		for j := range results[i].Content {
			if text := results[i].Content[j].Text; len(text) > currentToolResultMaxBytes {
				results[i].Content[j].Text = compactText(text, head, tail)
			}
		}
	}
}

func imageFingerprint(img KiroImage) [32]byte {
	return sha256.Sum256([]byte(img.Format + "\x00" + img.Source.Bytes))
}

// dedupeKiroImages keeps only the latest copy of each identical image. An
// earlier copy is replaced by a short note. The latest copy is kept because
// size control drops old turns and old images first.
func dedupeKiroImages(payload *KiroPayload) {
	if payload == nil || !config.GetCompatConfig().ImageDedup {
		return
	}
	seen := make(map[[32]byte]bool)
	dedupe := func(msg *KiroUserInputMessage) {
		if msg == nil || len(msg.Images) == 0 {
			return
		}
		kept := msg.Images[:0:0]
		dropped := 0
		for _, img := range msg.Images {
			fp := imageFingerprint(img)
			if seen[fp] {
				dropped++
				continue
			}
			seen[fp] = true
			kept = append(kept, img)
		}
		if dropped == 0 {
			return
		}
		msg.Images = kept
		msg.Content = joinHistoryText(msg.Content, duplicateImagePlaceholder)
	}

	dedupe(&payload.ConversationState.CurrentMessage.UserInputMessage)
	// Turns past the image cut are left alone: their images are dropped with a
	// note that must not change once sent (re-attaching one of their images
	// rewrote it, 243fc8a2). With a conversation ID that is the remembered cut
	// (0 until the images first pass their cap), so the turns deduped stay the
	// same from turn to turn; without one, the fixed window.
	history := payload.ConversationState.History
	aged := agedImageUserTurns(history)
	if id := payload.ConversationState.ConversationID; id != "" {
		aged = rememberedImageCut(id)
	}
	userTurn := historyUserTurns(history)
	for i := len(history) - 1; i >= 0 && userTurn > aged; i-- {
		if msg := history[i].UserInputMessage; msg != nil {
			dedupe(msg)
			userTurn--
		}
	}
}

func historyUserTurns(history []KiroHistoryMessage) int {
	n := 0
	for _, entry := range history {
		if entry.UserInputMessage != nil {
			n++
		}
	}
	return n
}

// agedImageUserTurns is how many history user turns, oldest first and the
// system priming included, lose their images when the payload is over the limit.
func agedImageUserTurns(history []KiroHistoryMessage) int {
	n := historyUserTurns(history)
	if n <= historyImageMinKeepUserTurns {
		return 0
	}
	return (n - historyImageMinKeepUserTurns) / historyImageWindowStep * historyImageWindowStep
}

type imageCut struct {
	aged int // history user turns, oldest first, that lost their images
	seen time.Time
}

var (
	imageCutsMu sync.Mutex
	imageCuts   = make(map[string]imageCut)
)

// maxPayloadImageBytes caps the base64 image data of a Kiro request. Kiro's
// CONTENT_LENGTH_EXCEEDS_THRESHOLD counts text, not image data: on CA2
// (2026-10-27) 8 images of ~400 KB added 0.16% context usage, and requests
// with 3.2–5.6 MB of history images passed with every image read (as
// minpeter/kiro-lb measured). So maxPayloadBytes() is compared with the
// payload without image data, and images only have to fit this cap. 5.6 MB
// was the largest total tested; above this cap old images are cut.
var maxPayloadImageBytes = 6 << 20

// imageDataBytes is the base64 image data size of images, as serialized.
func imageDataBytes(images []KiroImage) int {
	n := 0
	for _, img := range images {
		n += len(img.Source.Bytes)
	}
	return n
}

// payloadImageBytes is the base64 image data size of the whole payload.
func payloadImageBytes(payload *KiroPayload) int {
	n := imageDataBytes(payload.ConversationState.CurrentMessage.UserInputMessage.Images)
	for _, entry := range payload.ConversationState.History {
		if msg := entry.UserInputMessage; msg != nil {
			n += imageDataBytes(msg.Images)
		}
	}
	return n
}

// payloadTextBytes is the serialized payload size without image data, the
// size Kiro's input limit applies to.
func payloadTextBytes(payload *KiroPayload) int {
	return payloadByteSize(payload) - payloadImageBytes(payload)
}

// historyEntryTextSize is historyEntryByteSize without image data.
func historyEntryTextSize(entry KiroHistoryMessage) int {
	size := historyEntryByteSize(entry)
	if msg := entry.UserInputMessage; msg != nil {
		size -= imageDataBytes(msg.Images)
	}
	return size
}

// rememberedImageCut is the conversation's current image cut, or 0.
func rememberedImageCut(conversationID string) int {
	if conversationID == "" {
		return 0
	}
	imageCutsMu.Lock()
	defer imageCutsMu.Unlock()
	c, ok := imageCuts[conversationID]
	if !ok || time.Since(c.seen) > imageCutTTL {
		return 0
	}
	return c.aged
}

// chooseImageCut returns how many history user turns lose their images when
// the payload's image data (imageTotal bytes) is over maxPayloadImageBytes.
// Without a conversation ID it is the fixed window (agedImageUserTurns). With
// one, the remembered cut is kept while the images fit with it; otherwise the
// cut moves forward until they are under imageCutTargetFraction of the cap,
// keeping images on at least historyImageMinKeepUserTurns history user turns.
func chooseImageCut(conversationID string, history []KiroHistoryMessage, imageTotal int) int {
	if conversationID == "" {
		return agedImageUserTurns(history)
	}
	var imageBytes []int // per history user turn
	for _, entry := range history {
		if msg := entry.UserInputMessage; msg != nil {
			imageBytes = append(imageBytes, imageDataBytes(msg.Images))
		}
	}
	maxAged := len(imageBytes) - historyImageMinKeepUserTurns
	if maxAged <= 0 {
		return 0
	}
	sizeWith := func(aged int) int {
		s := imageTotal
		for _, b := range imageBytes[:aged] {
			s -= b
		}
		return s
	}
	aged := minInt(rememberedImageCut(conversationID), maxAged)
	if sizeWith(aged) > maxPayloadImageBytes {
		target := int(float64(maxPayloadImageBytes) * imageCutTargetFraction)
		for aged < maxAged && sizeWith(aged) > target {
			aged++
		}
	}
	rememberImageCut(conversationID, aged)
	return aged
}

func rememberImageCut(conversationID string, aged int) {
	now := time.Now()
	imageCutsMu.Lock()
	defer imageCutsMu.Unlock()
	imageCuts[conversationID] = imageCut{aged: aged, seen: now}
	if len(imageCuts) <= imageCutMax {
		return
	}
	for id, c := range imageCuts {
		if now.Sub(c.seen) > imageCutTTL || id != conversationID {
			delete(imageCuts, id)
			if len(imageCuts) <= imageCutMax {
				return
			}
		}
	}
}

// dropOldHistoryImages removes images from the first aged history user turns
// (oldest first, the system priming included). It reports whether anything
// changed.
func dropOldHistoryImages(history []KiroHistoryMessage, primingCount, aged int) bool {
	changed := false
	userTurn := 0
	for i := 0; i < len(history) && userTurn < aged; i++ {
		msg := history[i].UserInputMessage
		if msg == nil {
			continue
		}
		userTurn++
		if i < primingCount || len(msg.Images) == 0 {
			continue
		}
		msg.Content = joinHistoryText(msg.Content, fmt.Sprintf(oldHistoryImagesFormat, len(msg.Images)))
		msg.Images = nil
		changed = true
	}
	return changed
}

// compactHistoryText cuts very large history user text and structured tool
// results to their head and tail. The system priming is left intact.
func compactHistoryText(history []KiroHistoryMessage, primingCount int) bool {
	changed := false
	for i := primingCount; i < len(history); i++ {
		msg := history[i].UserInputMessage
		if msg == nil {
			continue
		}
		if len(msg.Content) > historyTextCompactLimit {
			msg.Content = compactText(msg.Content, historyTextKeepHead, historyTextKeepTail)
			changed = true
		}
		if msg.UserInputMessageContext == nil {
			continue
		}
		for r := range msg.UserInputMessageContext.ToolResults {
			content := msg.UserInputMessageContext.ToolResults[r].Content
			for c := range content {
				if len(content[c].Text) > historyTextCompactLimit {
					content[c].Text = compactText(content[c].Text, historyTextKeepHead, historyTextKeepTail)
					changed = true
				}
			}
		}
	}
	return changed
}

// sniffImageFormat returns the Kiro image format detected from the decoded
// bytes, or "" when it is not one of the formats Kiro accepts.
func sniffImageFormat(decoded []byte) string {
	if len(decoded) > 512 {
		decoded = decoded[:512]
	}
	switch http.DetectContentType(decoded) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpeg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}

// decodeBase64Image accepts the standard, raw, and URL-safe alphabets.
func decodeBase64Image(data string) ([]byte, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := enc.DecodeString(data); err == nil {
			return decoded, true
		}
	}
	return nil, false
}
