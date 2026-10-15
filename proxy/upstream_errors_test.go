package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kiro-go/auth"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyKiroHTTPError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   upstreamErrorClass
	}{
		{"plain 400", 400, `{"message":"Improperly formed request.","reason":null}`, upstreamErrClientInvalid},
		{"tool mismatch as 503", 503, `{"message":"bad","reason":"TOOL_USE_RESULT_MISMATCH"}`, upstreamErrClientInvalid},
		{"tool mismatch text", 500, `Expected toolResult blocks at messages.3`, upstreamErrClientInvalid},
		{"content length", 400, `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`, upstreamErrContextTooLong},
		{"profile required", 400, `{"message":"profileArn is required for this request."}`, upstreamErrProfileRequired},
		{"invalid model", 400, `{"message":"Invalid model. Please select a different model."}`, upstreamErrInvalidModel},
		{"monthly quota", 402, `{"message":"You have reached the limit.","reason":"MONTHLY_REQUEST_COUNT"}`, upstreamErrQuotaExhausted},
		{"overage", 402, `{"message":"limit","reason":"OVERAGE_REQUEST_LIMIT_EXCEEDED"}`, upstreamErrOverageLimit},
		{"reason only in message text", 402, `{"message":"MONTHLY_REQUEST_COUNT mentioned","reason":"OTHER"}`, upstreamErrOverageLimit},
		{"401", 401, `{"message":"Unauthorized"}`, upstreamErrTokenInvalid},
		{"403 bearer invalid", 403, `{"message":"The bearer token included in the request is invalid."}`, upstreamErrTokenInvalid},
		{"403 suspended", 403, `{"reason":"TEMPORARILY_SUSPENDED"}`, upstreamErrSuspended},
		{"403 other", 403, `{"message":"User is not authorized to make this call."}`, upstreamErrAuth},
		{"429 suspicious", 429, `Due to suspicious activity, we are imposing temporary limits on how frequently your account (d-123) can send a request`, upstreamErrAccountThrottled},
		{"429 plain", 429, `{"message":"Too many requests"}`, upstreamErrRateLimited},
		{"524", 524, `error code: 524`, upstreamErrGatewayTimeout},
		{"500", 500, `internal`, upstreamErrTransient},
		{"408", 408, `timeout`, upstreamErrTransient},
		{"404", 404, `not found`, upstreamErrClientInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyKiroHTTPError(tc.status, tc.body); got != tc.want {
				t.Fatalf("classify(%d, %q) = %s, want %s", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestClassifyUpstreamErrorNonHTTP(t *testing.T) {
	if got := classifyUpstreamError(errUpstreamTruncatedResponse); got != upstreamErrIntegrity {
		t.Fatalf("integrity error classified as %s", got)
	}
	streamErr := fmt.Errorf("%w: ValidationException: Expected toolResult blocks", errKiroEventStreamUpstream)
	if got := classifyUpstreamError(streamErr); got != upstreamErrClientInvalid {
		t.Fatalf("in-stream validation error classified as %s", got)
	}
	if got := classifyUpstreamError(fmt.Errorf("%w: boom", errKiroEventStreamUpstream)); got != upstreamErrTransient {
		t.Fatalf("in-stream upstream error classified as %s", got)
	}
	if got := classifyUpstreamError(errors.New("refresh token expired")); got != upstreamErrOther {
		t.Fatalf("plain error classified as %s", got)
	}
	wrapped := fmt.Errorf("wrapped: %w", &kiroHTTPError{StatusCode: 400, Endpoint: "x", Body: "{}"})
	if got := classifyUpstreamError(wrapped); got != upstreamErrClientInvalid {
		t.Fatalf("wrapped HTTP error classified as %s", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	if got := parseRetryAfter("30", now); got != 30*time.Second {
		t.Fatalf("seconds form = %s", got)
	}
	if got := parseRetryAfter(now.Add(2*time.Minute).Format(http.TimeFormat), now); got != 2*time.Minute {
		t.Fatalf("date form = %s", got)
	}
	if got := parseRetryAfter("999999", now); got != time.Hour {
		t.Fatalf("value must be capped at one hour, got %s", got)
	}
	for _, v := range []string{"", "soon", "-5"} {
		if got := parseRetryAfter(v, now); got != 0 {
			t.Fatalf("parseRetryAfter(%q) = %s, want 0", v, got)
		}
	}
}

func TestUpstreamClientErrorMapping(t *testing.T) {
	httpErr := func(status int, body string) error {
		return &kiroHTTPError{StatusCode: status, Endpoint: "Kiro IDE", Body: body}
	}
	cases := []struct {
		name       string
		err        error
		status     int
		errType    string
		msgPrefix  string
		retryAfter time.Duration
	}{
		{"bad request", httpErr(400, `{"message":"Improperly formed request.","reason":null}`), 400, "invalid_request_error", "Upstream rejected the request: Improperly formed request.", 0},
		{"tool mismatch", httpErr(503, `{"reason":"TOOL_USE_RESULT_MISMATCH"}`), 400, "invalid_request_error", "Invalid message sequence", 0},
		{"too long", httpErr(400, `{"reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`), 400, "invalid_request_error", "prompt is too long", 0},
		{"rate limited", &kiroHTTPError{StatusCode: 429, Body: "slow down", RetryAfter: 45 * time.Second}, 429, "rate_limit_error", "All upstream accounts", 45 * time.Second},
		{"quota", httpErr(402, `{"reason":"MONTHLY_REQUEST_COUNT"}`), 429, "rate_limit_error", "All upstream accounts", time.Minute},
		{"transient", httpErr(500, "boom"), 503, "overloaded_error", "Upstream is temporarily unavailable", 0},
		{"integrity", errUpstreamTruncatedResponse, 503, "overloaded_error", "Upstream truncated the response", 0},
		{"auth", httpErr(403, `{"message":"no"}`), 502, "api_error", "Upstream rejected the account credentials", 0},
		{"other", errors.New("no available accounts"), 502, "api_error", "Upstream request failed", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := upstreamClientError(tc.err, 0)
			if resp.Status != tc.status || resp.Type != tc.errType {
				t.Fatalf("status/type = %d/%s, want %d/%s", resp.Status, resp.Type, tc.status, tc.errType)
			}
			if !strings.HasPrefix(resp.Message, tc.msgPrefix) {
				t.Fatalf("message = %q, want prefix %q", resp.Message, tc.msgPrefix)
			}
			if !strings.Contains(resp.Message, resp.RequestID) {
				t.Fatalf("message must carry the request id, got %q", resp.Message)
			}
			if resp.RetryAfter != tc.retryAfter {
				t.Fatalf("retryAfter = %s, want %s", resp.RetryAfter, tc.retryAfter)
			}
		})
	}

	// The raw upstream body must never be echoed: it can contain account IDs.
	leak := upstreamClientError(httpErr(429, "Due to suspicious activity, we are imposing temporary limits on account (d-9999)"), 0)
	if strings.Contains(leak.Message, "d-9999") {
		t.Fatalf("raw upstream body leaked to client: %q", leak.Message)
	}
	// Pool availability wins over a shorter upstream hint.
	if got := upstreamClientError(httpErr(429, "x"), 3*time.Minute).RetryAfter; got != 3*time.Minute {
		t.Fatalf("retryAfter must use pool availability, got %s", got)
	}
}

func TestCallKiroAPIRequestErrorsSkipEndpointFallback(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCalls int
	}{
		{"bad request", 400, `{"message":"Improperly formed request."}`, 1},
		{"tool mismatch as 503", 503, `{"reason":"TOOL_USE_RESULT_MISMATCH"}`, 1},
		{"account throttled", 429, `suspicious activity ... temporary limits`, 1},
		{"monthly quota", 402, `{"reason":"MONTHLY_REQUEST_COUNT"}`, 1},
		{"plain 429 tries next endpoint", 429, `{"message":"Too many requests"}`, 2},
		{"5xx tries next endpoint", 500, `boom`, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installKiroRetryTestEndpoints(t)
			var calls int
			installKiroStreamTestClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode: tc.status,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     http.Header{"Retry-After": []string{"12"}},
				}, nil
			}))

			err := CallKiroAPI(newKiroRetryTestOAuthAccount(), newKiroRetryTestPayload(), &KiroStreamCallback{})
			var httpErr *kiroHTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("expected *kiroHTTPError, got %T %v", err, err)
			}
			if httpErr.StatusCode != tc.status || httpErr.RetryAfter != 12*time.Second {
				t.Fatalf("error = %+v", httpErr)
			}
			if calls != tc.wantCalls {
				t.Fatalf("upstream calls = %d, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// failoverTestServer counts calls and answers each one with respond(n, req).
type failoverTestServer struct {
	mu     sync.Mutex
	tokens []string
}

func (s *failoverTestServer) record(r *http.Request) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = append(s.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return len(s.tokens)
}

func (s *failoverTestServer) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tokens...)
}

func writeKiroSuccess(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": text}))
	_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
}

// setupFailoverTest installs the given accounts and a fake upstream, and
// returns a handler on the reloaded pool. Cooldowns set by the test are
// cleared afterwards because the pool is a process-wide singleton.
func setupFailoverTest(t *testing.T, server *httptest.Server, accounts ...config.Account) *Handler {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	for _, acc := range accounts {
		acc.Enabled = true
		if err := config.AddAccount(acc); err != nil {
			t.Fatalf("add account %s: %v", acc.ID, err)
		}
	}
	if err := config.UpdatePreferredEndpoint("kiro"); err != nil {
		t.Fatalf("set preferred endpoint: %v", err)
	}
	if err := config.UpdateEndpointFallback(false); err != nil {
		t.Fatalf("disable endpoint fallback: %v", err)
	}
	t.Cleanup(swapKiroEndpointsForTest(t, server))
	oldRefresh := refreshAccountInfoAsync
	refreshAccountInfoAsync = func(*Handler, config.Account) {}
	t.Cleanup(func() { refreshAccountInfoAsync = oldRefresh })

	p := accountpool.GetPool()
	p.Reload()
	t.Cleanup(func() {
		for _, acc := range accounts {
			p.RecordSuccess(acc.ID)
		}
	})
	return &Handler{pool: p, promptCache: newPromptCacheTracker(defaultPromptCacheTTL)}
}

func postClaudeNonStream(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4.5",
		"max_tokens":100,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	rec := httptest.NewRecorder()
	h.handleClaudeMessages(rec, req)
	return rec
}

func decodeClaudeError(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Error.Type, body.Error.Message
}

func failoverAccounts(prefix string) []config.Account {
	return []config.Account{
		{ID: prefix + "-a", AccessToken: prefix + "-token-a", ProfileArn: "arn:aws:codewhisperer:profile/a"},
		{ID: prefix + "-b", AccessToken: prefix + "-token-b", ProfileArn: "arn:aws:codewhisperer:profile/b"},
	}
}

func TestClaudeBadRequestStopsWithoutTryingOtherAccounts(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		http.Error(w, `{"message":"Improperly formed request.","reason":null}`, http.StatusBadRequest)
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("badreq")...)

	rec := postClaudeNonStream(t, h)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if errType, msg := decodeClaudeError(t, rec); errType != "invalid_request_error" || !strings.Contains(msg, "Improperly formed request.") {
		t.Fatalf("error = %s %q", errType, msg)
	}
	if got := len(srv.calls()); got != 1 {
		t.Fatalf("a request-scoped 400 must not be retried on other accounts, calls=%d", got)
	}
	if wait := h.pool.NextAvailableIn("claude-sonnet-4.5"); wait != 0 {
		t.Fatalf("a client error must not cool down the account, wait=%s", wait)
	}
}

func TestClaudeToolMismatch503IsNotRetried(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		http.Error(w, `{"message":"bad sequence","reason":"TOOL_USE_RESULT_MISMATCH"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("mismatch")...)

	rec := postClaudeNonStream(t, h)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if got := len(srv.calls()); got != 1 {
		t.Fatalf("tool mismatch must not fan out across accounts, calls=%d", got)
	}
}

func TestClaudeAllAccountsRateLimitedReturns429WithRetryAfter(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.record(r)
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"message":"Too many requests"}`, http.StatusTooManyRequests)
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("ratelimit")...)

	rec := postClaudeNonStream(t, h)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if errType, _ := decodeClaudeError(t, rec); errType != "rate_limit_error" {
		t.Fatalf("error type = %s", errType)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "30" && ra != "29" {
		t.Fatalf("Retry-After = %q, want about 30", ra)
	}
	if got := len(srv.calls()); got != 2 {
		t.Fatalf("each account should be tried once, calls=%d", got)
	}
}

func TestClaudeAccountThrottleFailsOverToNextAccount(t *testing.T) {
	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if srv.record(r) == 1 {
			http.Error(w, "Due to suspicious activity, we are imposing temporary limits on your account", http.StatusTooManyRequests)
			return
		}
		writeKiroSuccess(t, w, "served by second account")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("throttle")...)

	rec := postClaudeNonStream(t, h)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "served by second account") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	calls := srv.calls()
	if len(calls) != 2 || calls[0] == calls[1] {
		t.Fatalf("expected one call per account, got %v", calls)
	}
	// The healthy account keeps the pool available; with it excluded, only the
	// throttled account is left, returned as the earliest-cooldown fallback.
	if wait := h.pool.NextAvailableIn("claude-sonnet-4.5"); wait != 0 {
		t.Fatalf("healthy account should still be available, wait=%s", wait)
	}
	excluded := map[string]bool{}
	for _, acc := range failoverAccounts("throttle") {
		if acc.AccessToken != calls[0] {
			excluded[acc.ID] = true
		}
	}
	if acc := h.pool.GetNextForModelExcluding("claude-sonnet-4.5", excluded); acc == nil || acc.AccessToken != calls[0] {
		t.Fatalf("expected the throttled account as the cooldown fallback, got %+v", acc)
	}
}

func TestClaudeInvalidBearerTokenRefreshesAndRetriesSameAccount(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"refreshed-token","refreshToken":"rotated-refresh","expiresIn":3600}`))
	}))
	defer authServer.Close()
	oldTokenURL := auth.GetOIDCTokenURLForTest()
	auth.SetOIDCTokenURLForTest(func(string) string { return authServer.URL })
	t.Cleanup(func() { auth.SetOIDCTokenURLForTest(oldTokenURL) })
	oldAuthClient := auth.SetGlobalAuthClientForTest(authServer.Client())
	t.Cleanup(func() { auth.SetGlobalAuthClientForTest(oldAuthClient) })

	srv := &failoverTestServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if srv.record(r) == 1 {
			http.Error(w, `{"message":"The bearer token included in the request is invalid."}`, http.StatusForbidden)
			return
		}
		writeKiroSuccess(t, w, "after refresh")
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, config.Account{
		ID:           "refresh-retry",
		AccessToken:  "stale-token",
		RefreshToken: "refresh-token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AuthMethod:   "idc",
		Region:       "us-east-1",
		ProfileArn:   "arn:aws:codewhisperer:profile/refresh",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})

	rec := postClaudeNonStream(t, h)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "after refresh") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls := srv.calls(); len(calls) != 2 || calls[0] != "stale-token" || calls[1] != "refreshed-token" {
		t.Fatalf("expected stale then refreshed token on the same account, got %v", calls)
	}
	for _, acc := range config.GetAccounts() {
		if acc.ID == "refresh-retry" && (!acc.Enabled || acc.BanStatus != "") {
			t.Fatalf("account must stay enabled after a successful refresh, got enabled=%v ban=%q", acc.Enabled, acc.BanStatus)
		}
	}
}

func TestOpenAIBadRequestMapsTo400(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"message":"Improperly formed request."}`, http.StatusBadRequest)
	}))
	defer server.Close()
	h := setupFailoverTest(t, server, failoverAccounts("openai400")...)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	h.handleOpenAIChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"invalid_request_error"`)) {
		t.Fatalf("expected invalid_request_error, got %s", rec.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestRecoverFromUpstreamErrorStopsOnRequestScopedErrors(t *testing.T) {
	h := &Handler{pool: accountpool.GetPool()}
	account := &config.Account{ID: "recover-stop"}
	for _, err := range []error{
		&kiroHTTPError{StatusCode: 400, Body: `{"message":"bad"}`},
		&kiroHTTPError{StatusCode: 400, Body: `{"reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`},
	} {
		if got := h.recoverFromUpstreamError(account, err, map[string]bool{}); got != upstreamStopRetrying {
			t.Fatalf("%v: action = %d, want stop", err, got)
		}
	}
	if got := h.recoverFromUpstreamError(account, errUpstreamTruncatedResponse, map[string]bool{}); got != upstreamTryNextAccount {
		t.Fatalf("integrity error must move to the next account, got %d", got)
	}
}
