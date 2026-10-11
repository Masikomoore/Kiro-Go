package proxy

import (
	"regexp"
	"strings"

	"kiro-go/config"
)

// outputRewriteRulesFn is the enabled regex rules applied to assistant text.
// Tests replace it so a case does not write the process-wide config file.
var outputRewriteRulesFn = func() []config.PromptFilterRule {
	return config.GetPromptFilterRules()
}

// simpleLiteral is a regex that is only an optional (?i) flag plus a literal.
// Those are the rules we can safely reassemble across stream chunks.
type simpleLiteral struct {
	text string
	fold bool
}

// outputRewriter applies enabled regex prompt-filter rules to assistant text
// and reasoning as they stream. Literal rules keep a short suffix so a brand
// token split across events, such as "K" + "iro", is still replaced.
type outputRewriter struct {
	regexes  []*regexp.Regexp
	replaces []string
	literals []simpleLiteral
	pending  [2]string
}

func newOutputRewriter(rules []config.PromptFilterRule) *outputRewriter {
	rw := &outputRewriter{}
	for _, rule := range rules {
		if !rule.Enabled || rule.Type != "regex" || rule.Match == "" {
			continue
		}
		re, err := regexp.Compile(rule.Match)
		if err != nil {
			continue
		}
		rw.regexes = append(rw.regexes, re)
		rw.replaces = append(rw.replaces, rule.Replace)
		if lit, ok := parseSimpleLiteral(rule.Match); ok {
			rw.literals = append(rw.literals, lit)
		}
	}
	return rw
}

func parseSimpleLiteral(match string) (simpleLiteral, bool) {
	fold := false
	if strings.HasPrefix(match, "(?i)") {
		fold = true
		match = match[len("(?i)"):]
	}
	if match == "" || strings.ContainsAny(match, `\.+*?()[]{}^$|`) {
		return simpleLiteral{}, false
	}
	return simpleLiteral{text: match, fold: fold}, true
}

func (r *outputRewriter) push(chunk string, thinking bool) string {
	if chunk == "" {
		return ""
	}
	if len(r.regexes) == 0 {
		return chunk
	}
	idx := pendingIndex(thinking)
	buf := r.pending[idx] + chunk
	hold := literalHoldSuffix(buf, r.literals)
	ready := buf
	if hold != "" {
		ready = strings.TrimSuffix(buf, hold)
	}
	r.pending[idx] = hold
	return r.apply(ready)
}

func (r *outputRewriter) flush(thinking bool) string {
	idx := pendingIndex(thinking)
	buf := r.pending[idx]
	r.pending[idx] = ""
	return r.apply(buf)
}

func (r *outputRewriter) apply(s string) string {
	if s == "" {
		return ""
	}
	for i, re := range r.regexes {
		s = re.ReplaceAllString(s, r.replaces[i])
	}
	return s
}

func pendingIndex(thinking bool) int {
	if thinking {
		return 1
	}
	return 0
}

// literalHoldSuffix returns the longest suffix that is a proper prefix of any
// simple literal. A completed match is not held.
func literalHoldSuffix(s string, literals []simpleLiteral) string {
	if s == "" || len(literals) == 0 {
		return ""
	}
	runes := []rune(s)
	maxHold := 0
	for _, lit := range literals {
		n := len([]rune(lit.text)) - 1
		if n > maxHold {
			maxHold = n
		}
	}
	if maxHold > len(runes) {
		maxHold = len(runes)
	}
	for n := maxHold; n >= 1; n-- {
		suffix := string(runes[len(runes)-n:])
		for _, lit := range literals {
			litRunes := []rune(lit.text)
			if n >= len(litRunes) {
				continue
			}
			prefix := string(litRunes[:n])
			if lit.fold {
				if strings.EqualFold(suffix, prefix) {
					return suffix
				}
			} else if suffix == prefix {
				return suffix
			}
		}
	}
	return ""
}
