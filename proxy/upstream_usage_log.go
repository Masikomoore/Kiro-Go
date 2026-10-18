package proxy

import (
	"kiro-go/logger"
	"sort"
	"strconv"
	"strings"
)

// Upstream usage logging.
//
// Kiro's event stream may carry token usage beyond credits: kirocc
// (d-kuro/kirocc, Apache-2.0) reads metadataEvent.tokenUsage with
// uncachedInputTokens, cacheReadInputTokens and cacheWriteInputTokens. Whether
// the endpoints kiro-go uses send them is unverified, so each upstream attempt
// logs one [KiroUsage] line with the event types seen and every numeric field
// of the usage-bearing events. Answer text is never logged.

// usageEventTypes are the events whose fields are logged.
var usageEventTypes = map[string]bool{
	"metadataEvent":     true,
	"meteringEvent":     true,
	"contextUsageEvent": true,
}

type upstreamUsageLog struct {
	counts map[string]int
	fields map[string]string
}

func newUpstreamUsageLog() *upstreamUsageLog {
	return &upstreamUsageLog{counts: make(map[string]int), fields: make(map[string]string)}
}

// observe is a KiroStreamCallback.OnRawEvent hook.
func (u *upstreamUsageLog) observe(eventType string, event map[string]interface{}) {
	u.counts[eventType]++
	if !usageEventTypes[eventType] {
		return
	}
	u.flatten(eventType, event)
}

// flatten records numeric leaves, and short strings such as stopReason, under
// their dotted path. A later event of the same type overwrites earlier values.
func (u *upstreamUsageLog) flatten(path string, v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, child := range t {
			u.flatten(path+"."+k, child)
		}
	case []interface{}:
		for i, child := range t {
			u.flatten(path+"["+strconv.Itoa(i)+"]", child)
		}
	case float64:
		u.fields[path] = strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		u.fields[path] = strconv.FormatBool(t)
	case string:
		if len(t) <= 40 {
			u.fields[path] = t
		}
	}
}

func (u *upstreamUsageLog) line(conv, endpoint string) string {
	types := make([]string, 0, len(u.counts))
	for k, n := range u.counts {
		types = append(types, k+":"+strconv.Itoa(n))
	}
	sort.Strings(types)
	keys := make([]string, 0, len(u.fields))
	for k := range u.fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + u.fields[k]
	}
	return "[KiroUsage] conv=" + conv + " endpoint=" + endpoint +
		" events=" + strings.Join(types, ",") + " fields=" + strings.Join(parts, " ")
}

func (u *upstreamUsageLog) log(conv, endpoint string) {
	logger.Infof("%s", u.line(conv, endpoint))
}
