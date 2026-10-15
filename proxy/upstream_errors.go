// Upstream error classification for Kiro generateAssistantResponse calls.
//
// Portions adapted from hank9999/kiro.rs (MIT): the error markers and the
// retry policy per class follow src/kiro/provider.rs and src/kiro/endpoint/mod.rs.
package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// kiroHTTPError is a non-200 response from a Kiro endpoint. Error() keeps the
// historical "HTTP <code> from <endpoint>: <body>" text so existing string
// matchers and logs stay unchanged.
type kiroHTTPError struct {
	StatusCode int
	Endpoint   string
	Body       string
	RetryAfter time.Duration
}

func (e *kiroHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d from %s: %s", e.StatusCode, e.Endpoint, e.Body)
}

func newKiroHTTPError(resp *http.Response, endpoint string, body []byte) *kiroHTTPError {
	return &kiroHTTPError{
		StatusCode: resp.StatusCode,
		Endpoint:   endpoint,
		Body:       string(body),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}

// parseRetryAfter accepts both delay-seconds and HTTP-date forms. Values are
// capped at one hour so a bogus header cannot park an account for days.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var d time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		d = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		d = at.Sub(now)
	}
	if d <= 0 {
		return 0
	}
	if d > time.Hour {
		return time.Hour
	}
	return d
}

type upstreamErrorClass int

const (
	upstreamErrOther upstreamErrorClass = iota
	// The request itself is malformed; every account would reject it.
	upstreamErrClientInvalid
	upstreamErrContextTooLong
	upstreamErrInvalidModel
	// Builder ID / free accounts that need a profileArn the request lacked.
	upstreamErrProfileRequired
	upstreamErrTokenInvalid
	upstreamErrAuth
	upstreamErrSuspended
	// 429 "suspicious activity ... temporary limits": only this account is limited.
	upstreamErrAccountThrottled
	upstreamErrQuotaExhausted
	upstreamErrOverageLimit
	upstreamErrRateLimited
	upstreamErrGatewayTimeout
	upstreamErrTransient
	upstreamErrIntegrity
	upstreamErrProfileUnavailable
)

func (c upstreamErrorClass) String() string {
	switch c {
	case upstreamErrClientInvalid:
		return "client_invalid"
	case upstreamErrContextTooLong:
		return "context_too_long"
	case upstreamErrInvalidModel:
		return "invalid_model"
	case upstreamErrProfileRequired:
		return "profile_required"
	case upstreamErrTokenInvalid:
		return "token_invalid"
	case upstreamErrAuth:
		return "auth"
	case upstreamErrSuspended:
		return "suspended"
	case upstreamErrAccountThrottled:
		return "account_throttled"
	case upstreamErrQuotaExhausted:
		return "quota_exhausted"
	case upstreamErrOverageLimit:
		return "overage_limit"
	case upstreamErrRateLimited:
		return "rate_limited"
	case upstreamErrGatewayTimeout:
		return "gateway_timeout"
	case upstreamErrTransient:
		return "transient"
	case upstreamErrIntegrity:
		return "stream_integrity"
	case upstreamErrProfileUnavailable:
		return "profile_unavailable"
	default:
		return "other"
	}
}

// isRequestScoped reports whether the failure is caused by the request rather
// than the account. Retrying it on another account cannot help and only
// multiplies one bad request into several upstream calls.
func (c upstreamErrorClass) isRequestScoped() bool {
	return c == upstreamErrClientInvalid || c == upstreamErrContextTooLong
}

// Exact reasons that mean the upstream billing period is exhausted for this
// credential. The overage variant is handled by the overage refresh path.
const (
	reasonMonthlyRequestCount = "MONTHLY_REQUEST_COUNT"
	reasonOverageLimit        = "OVERAGE_REQUEST_LIMIT_EXCEEDED"
)

// Request-shape violations. Bedrock sometimes reports these with a 5xx, which
// must not be retried: doing so turns one bad request into a burst of 503s.
var (
	clientValidationReasons = []string{"TOOL_USE_RESULT_MISMATCH"}
	clientValidationMarkers = []string{"Expected toolResult blocks"}
	contextTooLongMarkers   = []string{"CONTENT_LENGTH_EXCEEDS_THRESHOLD", "Input is too long"}
	invalidModelMarkers     = []string{"invalid model", "invalid_model_id", "model not supported", "unsupportedmodel", "unsupported model"}
)

func classifyUpstreamError(err error) upstreamErrorClass {
	if err == nil {
		return upstreamErrOther
	}
	if isStreamIntegrityError(err) {
		return upstreamErrIntegrity
	}

	var httpErr *kiroHTTPError
	if errors.As(err, &httpErr) {
		return classifyKiroHTTPError(httpErr.StatusCode, httpErr.Body)
	}

	msg := err.Error()
	switch {
	case hasClientValidationMarker(msg):
		return upstreamErrClientInvalid
	case containsAny(msg, contextTooLongMarkers):
		return upstreamErrContextTooLong
	case isProfileUnavailableErrorMessage(msg):
		return upstreamErrProfileUnavailable
	case errors.Is(err, errKiroEventStreamUpstream),
		errors.Is(err, errEmptyKiroStream),
		errors.Is(err, errInvalidKiroEventStream),
		errors.Is(err, errIncompleteKiroToolInput):
		return upstreamErrTransient
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return upstreamErrTransient
	}
	return upstreamErrOther
}

func classifyKiroHTTPError(status int, body string) upstreamErrorClass {
	lower := strings.ToLower(body)
	switch {
	case hasClientValidationMarker(body):
		return upstreamErrClientInvalid
	case containsAny(body, contextTooLongMarkers):
		return upstreamErrContextTooLong
	}

	switch {
	case status == http.StatusPaymentRequired:
		if hasUpstreamReason(body, reasonMonthlyRequestCount) {
			return upstreamErrQuotaExhausted
		}
		return upstreamErrOverageLimit
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if isSuspensionErrorMessage(body) || strings.Contains(body, "SUSPENDED") {
			return upstreamErrSuspended
		}
		if status == http.StatusUnauthorized || strings.Contains(body, "The bearer token included in the request is invalid") {
			return upstreamErrTokenInvalid
		}
		return upstreamErrAuth
	case status == http.StatusTooManyRequests:
		if strings.Contains(lower, "suspicious activity") && strings.Contains(lower, "temporary limits") {
			return upstreamErrAccountThrottled
		}
		return upstreamErrRateLimited
	case status == 524 || (strings.Contains(body, "524") && (strings.Contains(lower, "gateway timeout") || strings.Contains(lower, "server-side issue"))):
		return upstreamErrGatewayTimeout
	case status == http.StatusBadRequest:
		if strings.Contains(lower, "profilearn is required") {
			return upstreamErrProfileRequired
		}
		if containsAny(lower, invalidModelMarkers) {
			return upstreamErrInvalidModel
		}
		return upstreamErrClientInvalid
	case status == http.StatusRequestTimeout || status >= 500:
		return upstreamErrTransient
	case status >= 400:
		return upstreamErrClientInvalid
	}
	return upstreamErrOther
}

// hasUpstreamReason confirms a reason in the top-level or nested error.reason
// field so an incidental substring elsewhere does not match. Non-JSON bodies
// fall back to the substring itself.
func hasUpstreamReason(body, reason string) bool {
	if !strings.Contains(body, reason) {
		return false
	}
	var parsed struct {
		Reason string `json:"reason"`
		Error  struct {
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return true
	}
	return parsed.Reason == reason || parsed.Error.Reason == reason
}

func hasClientValidationMarker(body string) bool {
	for _, reason := range clientValidationReasons {
		if hasUpstreamReason(body, reason) {
			return true
		}
	}
	return containsAny(body, clientValidationMarkers)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// upstreamRetryAfter returns the Retry-After carried by an upstream error.
func upstreamRetryAfter(err error) time.Duration {
	var httpErr *kiroHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.RetryAfter
	}
	return 0
}

// upstreamErrorMessage extracts the upstream "message" field for client-caused
// errors. Raw bodies are never echoed: they can carry account and request IDs.
func upstreamErrorMessage(err error) string {
	var httpErr *kiroHTTPError
	if !errors.As(err, &httpErr) {
		return ""
	}
	var parsed struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &parsed) != nil {
		return ""
	}
	msg := strings.TrimSpace(parsed.Message)
	if msg == "" {
		msg = strings.TrimSpace(parsed.Error.Message)
	}
	return truncateUTF8Bytes(msg, 300)
}

// clientErrorResponse is what a caller sees for a failed upstream call.
type clientErrorResponse struct {
	Status     int
	Type       string
	Message    string
	RetryAfter time.Duration
	RequestID  string
}

// upstreamClientError maps the final upstream error to a client-facing status.
// availableIn is how long until an account for the model leaves cooldown.
func upstreamClientError(err error, availableIn time.Duration) clientErrorResponse {
	class := classifyUpstreamError(err)
	resp := clientErrorResponse{RequestID: "req_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]}
	detail := upstreamErrorMessage(err)
	withDetail := func(base string) string {
		if detail == "" {
			return base
		}
		return base + ": " + detail
	}

	switch class {
	case upstreamErrClientInvalid:
		resp.Status, resp.Type = http.StatusBadRequest, "invalid_request_error"
		if hasClientValidationMarker(err.Error()) {
			resp.Message = "Invalid message sequence: tool_use and tool_result blocks must be correctly paired and ordered."
		} else {
			resp.Message = withDetail("Upstream rejected the request")
		}
	case upstreamErrContextTooLong:
		resp.Status, resp.Type = http.StatusBadRequest, "invalid_request_error"
		resp.Message = "prompt is too long: the request exceeds the upstream input limit. Reduce conversation history, system prompt, tools, or images."
	case upstreamErrInvalidModel:
		resp.Status, resp.Type = http.StatusBadRequest, "invalid_request_error"
		resp.Message = withDetail("Model is not available upstream")
	case upstreamErrRateLimited, upstreamErrAccountThrottled, upstreamErrQuotaExhausted, upstreamErrOverageLimit:
		resp.Status, resp.Type = http.StatusTooManyRequests, "rate_limit_error"
		resp.Message = "All upstream accounts are rate limited or out of quota"
		resp.RetryAfter = availableIn
		if ra := upstreamRetryAfter(err); ra > resp.RetryAfter {
			resp.RetryAfter = ra
		}
		if resp.RetryAfter <= 0 {
			resp.RetryAfter = time.Minute
		}
	case upstreamErrTransient, upstreamErrGatewayTimeout, upstreamErrIntegrity:
		resp.Status, resp.Type = http.StatusServiceUnavailable, "overloaded_error"
		if class == upstreamErrIntegrity {
			resp.Message = "Upstream truncated the response"
		} else {
			resp.Message = "Upstream is temporarily unavailable"
		}
	case upstreamErrTokenInvalid, upstreamErrAuth, upstreamErrSuspended:
		resp.Status, resp.Type = http.StatusBadGateway, "api_error"
		resp.Message = "Upstream rejected the account credentials"
	default:
		resp.Status, resp.Type = http.StatusBadGateway, "api_error"
		resp.Message = "Upstream request failed"
	}
	resp.Message = fmt.Sprintf("%s (request_id: %s)", resp.Message, resp.RequestID)
	return resp
}

func setRetryAfterHeader(w http.ResponseWriter, d time.Duration) {
	if d <= 0 {
		return
	}
	seconds := int((d + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
}
