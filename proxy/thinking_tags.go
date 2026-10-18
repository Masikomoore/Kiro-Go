// <thinking> tag detection for models that stream reasoning inline.
//
// A tag counts only when it is not wrapped in a backtick or quote, and a start
// tag also must not sit inside a markdown code fence. Otherwise an answer that
// merely mentions `<thinking>` would be swallowed into a thinking block.
// Portions adapted from hank9999/kiro.rs (MIT), src/anthropic/stream.rs.
package proxy

import (
	"strings"
	"unicode/utf8"
)

const (
	thinkingOpenTag  = "<thinking>"
	thinkingCloseTag = "</thinking>"
)

type tagVerdict int

const (
	tagNotReal tagVerdict = iota
	tagReal
	// tagPending: the tag ends the buffer, so the next character decides.
	tagPending
)

func isThinkingQuoteChar(c byte) bool {
	return c == '`' || c == '"' || c == '\''
}

func judgeThinkingTag(buf string, pos, tagLen int, final bool) tagVerdict {
	if pos > 0 && isThinkingQuoteChar(buf[pos-1]) {
		return tagNotReal
	}
	after := pos + tagLen
	if after >= len(buf) {
		if final {
			return tagReal
		}
		return tagPending
	}
	if isThinkingQuoteChar(buf[after]) {
		return tagNotReal
	}
	return tagReal
}

// thinkingTagScanner tracks whether answer text already emitted ended inside
// an open code fence.
type thinkingTagScanner struct {
	inFence bool
}

// findStart returns the index of a real <thinking> start tag in buf, or -1
// when there is none yet. final means no more text will follow.
func (s *thinkingTagScanner) findStart(buf string, final bool) int {
	from := 0
	for {
		i := strings.Index(buf[from:], thinkingOpenTag)
		if i < 0 {
			return -1
		}
		pos := from + i
		inFence := s.inFence != (strings.Count(buf[:pos], markdownFence)%2 == 1)
		if !inFence {
			switch judgeThinkingTag(buf, pos, len(thinkingOpenTag), final) {
			case tagReal:
				return pos
			case tagPending:
				return -1
			}
		}
		from = pos + 1
	}
}

// findEnd returns the index of a real </thinking> tag in buf, or -1.
func (s *thinkingTagScanner) findEnd(buf string, final bool) int {
	from := 0
	for {
		i := strings.Index(buf[from:], thinkingCloseTag)
		if i < 0 {
			return -1
		}
		pos := from + i
		switch judgeThinkingTag(buf, pos, len(thinkingCloseTag), final) {
		case tagReal:
			return pos
		case tagPending:
			return -1
		}
		from = pos + 1
	}
}

// emitted records answer text sent to the client so fence state carries over
// to later chunks.
func (s *thinkingTagScanner) emitted(text string) {
	if strings.Count(text, markdownFence)%2 == 1 {
		s.inFence = !s.inFence
	}
}

// answerFlushLen returns how many leading bytes of buf can go out as answer
// text while no start tag has been found. Only the last len(thinkingOpenTag)
// bytes are held: they may be the front of a split tag, and they keep the
// character before a tag available for the quote check. kiro-RS-Tool holds
// back the same amount; the old 50-rune minimum delayed every short chunk.
func answerFlushLen(buf string, final bool) int {
	if final {
		return len(buf)
	}
	n := len(buf) - len(thinkingOpenTag)
	if n <= 0 {
		return 0
	}
	for n > 0 && !utf8.RuneStart(buf[n]) {
		n--
	}
	return n
}
