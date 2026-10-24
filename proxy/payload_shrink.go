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
	// Turns past the image cut are left alone. Over the limit their images are
	// dropped with a note that must not change once sent (re-attaching one of
	// their images rewrote it, 243fc8a2); under it, an old duplicate costs less
	// than Kiro writing the history after it to its cache again.
	history := payload.ConversationState.History
	aged := agedImageUserTurns(history)
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

// dropOldHistoryImages removes images from the user turns before the image
// cut (agedImageUserTurns). It reports whether anything changed.
func dropOldHistoryImages(history []KiroHistoryMessage, primingCount int) bool {
	changed := false
	aged := agedImageUserTurns(history)
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
