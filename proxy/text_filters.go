// Stream text filters applied to assistant answer text before output rewrite.
//
// Portions adapted from hank9999/kiro.rs (MIT): the tool_use XML leak rules
// follow src/anthropic/stream.rs.
package proxy

import "strings"

const (
	toolUseXMLOpen  = "<tool_use"
	toolUseXMLClose = "</tool_use>"
	markdownFence   = "```"
)

// toolUseXMLFilter removes <tool_use ...>...</tool_use> fragments that the
// upstream sometimes leaks into answer text next to the real structured tool
// call. A client would otherwise show raw tool XML as prose. Tags inside a
// markdown code fence are left alone, so an answer that explains such XML in
// a code block is not damaged. Input arrives in arbitrary chunks, so partial
// markers at a chunk end are held back until the next chunk.
type toolUseXMLFilter struct {
	enabled   bool
	buf       string
	stripping bool
	inFence   bool
}

func newToolUseXMLFilter(enabled bool) *toolUseXMLFilter {
	return &toolUseXMLFilter{enabled: enabled}
}

func (f *toolUseXMLFilter) push(chunk string) string {
	if !f.enabled {
		return chunk
	}
	f.buf += chunk
	var out strings.Builder
	for {
		if f.stripping {
			end := strings.Index(f.buf, toolUseXMLClose)
			if end < 0 {
				// Everything here is leaked XML; keep only enough to find a
				// close tag split across chunks.
				if keep := len(toolUseXMLClose) - 1; len(f.buf) > keep {
					f.buf = f.buf[len(f.buf)-keep:]
				}
				return out.String()
			}
			f.buf = f.buf[end+len(toolUseXMLClose):]
			f.stripping = false
			continue
		}

		fence := strings.Index(f.buf, markdownFence)
		open := strings.Index(f.buf, toolUseXMLOpen)
		if fence >= 0 && (open < 0 || fence < open) {
			out.WriteString(f.buf[:fence+len(markdownFence)])
			f.buf = f.buf[fence+len(markdownFence):]
			f.inFence = !f.inFence
			continue
		}
		if open < 0 {
			hold := heldMarkerPrefix(f.buf)
			out.WriteString(f.buf[:len(f.buf)-hold])
			f.buf = f.buf[len(f.buf)-hold:]
			return out.String()
		}

		after := open + len(toolUseXMLOpen)
		if after == len(f.buf) {
			// "<tool_use" at the chunk end: wait to see the next character.
			out.WriteString(f.buf[:open])
			f.buf = f.buf[open:]
			return out.String()
		}
		next := f.buf[after]
		isTag := next == '>' || next == ' ' || next == '\t' || next == '\n' || next == '\r'
		if f.inFence || !isTag {
			out.WriteString(f.buf[:after])
			f.buf = f.buf[after:]
			continue
		}
		out.WriteString(f.buf[:open])
		f.buf = f.buf[open:]
		f.stripping = true
	}
}

// flush returns held text at end of stream. An unterminated leak is dropped.
func (f *toolUseXMLFilter) flush() string {
	if !f.enabled {
		return ""
	}
	rest := f.buf
	f.buf = ""
	if f.stripping {
		f.stripping = false
		return ""
	}
	return rest
}

// heldMarkerPrefix returns how many trailing bytes could begin a marker.
func heldMarkerPrefix(s string) int {
	held := 0
	for _, marker := range []string{toolUseXMLOpen, markdownFence} {
		maxLen := len(marker) - 1
		if maxLen > len(s) {
			maxLen = len(s)
		}
		for n := maxLen; n > held; n-- {
			if strings.HasPrefix(marker, s[len(s)-n:]) {
				held = n
				break
			}
		}
	}
	return held
}
