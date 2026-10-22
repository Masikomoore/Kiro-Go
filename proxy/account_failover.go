package proxy

import (
	"errors"
	"kiro-go/config"
	"kiro-go/logger"
	"net/url"
	"strings"
	"time"
)

const maxAccountRetryAttempts = 3

// maxSameAccountStreamRetries bounds same-account recovery of a truncated
// stream. Kiro IDE caps its truncation retry at one (Dt3 = 1 in extension.js),
// but that budget was set for a single-credential client. This proxy also
// rotates accounts, and the two recover different failures: a same-account
// retry helps when the upstream hiccupped, rotation helps when that account's
// backend is unhealthy. Two is therefore not a copy of the IDE.
//
// Cost of the extra attempt is bounded and small. Truncation returns nil from
// CallKiroAPIContext, so it never reaches the endpoint fallback or
// maxStreamAttemptsPerEndpoint - those fire only on transport errors. Worst
// case across maxAccountRetryAttempts accounts is 3*(1+budget) requests: 9 at
// two retries versus 6 at one.
//
// The payoff lands mostly on the three fully buffered paths, whose canRetry is
// nil: a non-stream client gets a 500 with nothing usable, so one more chance
// is worth more there than on a stream that has already flushed partial text.
const maxSameAccountStreamRetries = 2

// errUpstreamTruncatedResponse is a soft failure raised when a transport-clean
// stream carried content but never a terminal signal. It is retryable on the
// same account and must not mark the account unhealthy.
//
// There is deliberately no empty-response error here. A stream that produced no
// output at all is already caught one layer down: parseEventStreamTracked
// returns errEmptyKiroStream when !sawOutput (proxy/kiro.go), and
// CallKiroAPIContext retries it internally. Since sawOutput is set by exactly
// the three signals classifyStreamIntegrity measures (content, reasoning,
// toolUse), an all-zero measurement can never reach this layer with a nil
// error.
var errUpstreamTruncatedResponse = errors.New("upstream truncated response without stop reason")

// classifyStreamIntegrity decides whether an upstream stream that returned no
// transport error is actually complete. parseEventStream reports success on a
// clean EOF, so a stream that died mid-answer is otherwise indistinguishable
// from a finished one.
//
// Complete when a stopReason arrived, or when a tool call was delivered. Both
// match Kiro IDE, whose empty and truncation predicates each require
// toolCallCount === 0.
//
// Truncated when content arrived without any terminal signal.
//
// Reasoning-only with no answer is STRICTER THAN THE IDE, deliberately. The
// IDE's truncation predicate ends in (contentChars > 0 || !reasoningSeen), so
// reasoning with no answer and no stopReason is treated as complete there and
// is never retried. That is the exact shape of the production symptom this
// proxy exists to fix: thinking streams in full, then the turn dies before the
// answer or the tool call. Handing a client reasoning with no answer as a
// successful turn is what made the failure invisible, so it is classified as
// truncated here.
func classifyStreamIntegrity(contentChars, toolCallCount int, stopReason string, sawReasoning bool) error {
	if strings.TrimSpace(stopReason) != "" {
		return nil
	}
	if toolCallCount > 0 {
		return nil
	}
	if contentChars > 0 || sawReasoning {
		return errUpstreamTruncatedResponse
	}
	// No content, no reasoning, no tools: unreachable through the wired paths
	// (errEmptyKiroStream fires first, see above). Treated as truncated rather
	// than complete so a future caller that bypasses that guard still cannot
	// ship an empty turn as a success.
	return errUpstreamTruncatedResponse
}

// isStreamIntegrityError reports whether err is a soft integrity failure.
// Callers may rotate accounts on these, but must not run them through
// handleAccountFailure: an upstream blip should not mark an account unhealthy.
func isStreamIntegrityError(err error) bool {
	return errors.Is(err, errUpstreamTruncatedResponse)
}

func isQuotaErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "429") || strings.Contains(msg, "quota")
}

func isOverageErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "402") && strings.Contains(msg, "overage")
}

func isSuspensionErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "temporarily_suspended") ||
		strings.Contains(msg, "temporarily is suspended") ||
		strings.Contains(msg, "account suspended")
}

func isProfileUnavailableErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "no available kiro profile")
}

func isAuthErrorMessage(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "http 401") ||
		strings.Contains(msg, "http 403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "token invalid") ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "access token expired") ||
		strings.Contains(msg, "refresh token expired")
}

func (h *Handler) disableAccount(account *config.Account, banStatus, banReason string) {
	if account == nil {
		return
	}

	if !account.Enabled && account.BanStatus == banStatus && account.BanReason == banReason {
		return
	}

	if err := config.SetAccountBanStatus(account.ID, banStatus, banReason); err != nil {
		logger.Warnf("[AccountFailover] Failed to disable %s: %v", account.Email, err)
		return
	}

	logger.Warnf("[AccountFailover] Disabled %s: %s", account.Email, banReason)
	h.pool.Reload()
}

func (h *Handler) disableAccountOverage(account *config.Account) {
	if account == nil {
		return
	}

	snap, fetchErr := FetchOverageStatus(account)
	if fetchErr != nil {
		logger.Warnf("[AccountFailover] Failed to refresh overage status for %s: %v", account.Email, fetchErr)
		return
	}
	if persistErr := PersistOverageSnapshot(account.ID, snap); persistErr != nil {
		logger.Warnf("[AccountFailover] Failed to persist overage snapshot for %s: %v", account.Email, persistErr)
		return
	}

	logger.Warnf("[AccountFailover] Refreshed overage status for %s after upstream overage limit error: %s", account.Email, snap.Status)
	h.pool.Reload()
}

// authFailureCooldown parks an account after a 401/403 that is neither an
// invalid bearer token nor a suspension. Such responses are often transient
// permission or propagation errors, so the account is not banned.
const authFailureCooldown = 10 * time.Minute

// refreshAccountInfoAsync re-reads usage limits after a quota error so the pool
// sees the exhausted quota and its reset date. Tests replace it.
var refreshAccountInfoAsync = func(h *Handler, account config.Account) {
	go func() {
		info, err := RefreshAccountInfo(&account)
		if err != nil {
			logger.Warnf("[AccountFailover] Failed to refresh usage for %s after quota error: %v", account.Email, err)
			return
		}
		config.UpdateAccountInfo(account.ID, *info)
		h.pool.Reload()
	}()
}

// handleAccountFailure updates account state after a failed upstream call.
// Request-scoped failures leave the account untouched.
func (h *Handler) handleAccountFailure(account *config.Account, err error) {
	if account == nil || err == nil {
		return
	}
	// An egress failure says the account's proxy is unreachable, not that the
	// account is bad: open the proxy breaker and leave the account alone.
	if h.markProxyEgressFailure(account, err) {
		return
	}

	switch classifyUpstreamError(err) {
	case upstreamErrIntegrity, upstreamErrClientInvalid, upstreamErrContextTooLong,
		upstreamErrInvalidModel, upstreamErrProfileRequired:
		return
	case upstreamErrSuspended:
		h.disableAccount(account, "BANNED", "AWS temporarily suspended - unusual user activity detected")
	case upstreamErrTokenInvalid:
		h.disableAccount(account, "BANNED", "Authentication failed - token invalid or expired")
	case upstreamErrAuth:
		h.pool.SetCooldown(account.ID, time.Now().Add(authFailureCooldown))
	case upstreamErrAccountThrottled:
		cooldown := config.GetCompatConfig().AccountThrottleCooldown
		h.pool.SetCooldown(account.ID, time.Now().Add(cooldown))
		logger.Warnf("[AccountFailover] %s throttled by upstream (suspicious activity); cooling down %s", account.Email, cooldown)
	case upstreamErrQuotaExhausted:
		h.pool.MarkOverLimit(account.ID)
		refreshAccountInfoAsync(h, *account)
	case upstreamErrOverageLimit:
		h.disableAccountOverage(account)
		h.pool.RecordError(account.ID, false)
	case upstreamErrRateLimited:
		h.pool.RecordRateLimited(account.ID, upstreamRetryAfter(err))
	case upstreamErrProfileUnavailable, upstreamErrTransient, upstreamErrGatewayTimeout:
		// Profile ARN may be transiently unresolvable and upstream faults are
		// not the credential's fault: short consecutive-error cooldown only.
		h.pool.RecordError(account.ID, false)
	default:
		h.handleAccountFailureMessage(account, err.Error())
	}
}

// handleAccountFailureMessage covers errors that carry no HTTP status, such as
// token refresh failures, by matching their text.
func (h *Handler) handleAccountFailureMessage(account *config.Account, errMsg string) {
	switch {
	case isOverageErrorMessage(errMsg):
		h.disableAccountOverage(account)
		h.pool.RecordError(account.ID, false)
	case isQuotaErrorMessage(errMsg):
		h.pool.RecordError(account.ID, true)
	case isSuspensionErrorMessage(errMsg):
		h.disableAccount(account, "BANNED", "AWS temporarily suspended - unusual user activity detected")
	case isProfileUnavailableErrorMessage(errMsg):
		h.pool.RecordError(account.ID, false)
	case isAuthErrorMessage(errMsg):
		h.disableAccount(account, "BANNED", "Authentication failed - token invalid or expired")
	default:
		h.pool.RecordError(account.ID, false)
	}
}

// markProxyEgressFailure opens the breaker on the account's proxy when err is
// an egress failure behind that proxy. Every account on the same proxy is then
// skipped until the breaker closes. It reports whether err was such a failure.
// A direct account (no proxy) is not handled here.
func (h *Handler) markProxyEgressFailure(account *config.Account, err error) bool {
	if !isEgressFailure(err) {
		return false
	}
	proxyURL := ResolveAccountProxyURL(account)
	if proxyURL == "" {
		return false
	}
	until := time.Now().Add(config.GetCompatConfig().ProxyDownCooldown)
	lastErr := redactProxyUserinfo(err.Error(), proxyURL)
	if h.pool.MarkProxyDown(proxyURL, until, lastErr) {
		logger.Warnf("[ProxyHealth] %s down: %s; skipping %d accounts until %s",
			proxyDisplayName(proxyURL), lastErr, h.pool.AccountsBehindProxy(proxyURL), until.Format(time.RFC3339))
	}
	return true
}

// proxyDisplayName names a proxy for logs: its saved name, else scheme and host
// with any credentials left out.
func proxyDisplayName(proxyURL string) string {
	for _, p := range config.GetSavedProxies() {
		if p.URL == proxyURL {
			return p.Name
		}
	}
	if u, err := url.Parse(proxyURL); err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "proxy"
}

type upstreamAction int

const (
	upstreamTryNextAccount upstreamAction = iota
	upstreamRetrySameAccount
	upstreamStopRetrying
)

// recoverFromUpstreamError decides what an account loop does after a failed
// call. A request-scoped failure stops the loop: every account would reject
// the same request. An invalid bearer token gets one forced refresh per
// account and a retry on the same account before the account is penalized.
// refreshed records which accounts already used that refresh.
func (h *Handler) recoverFromUpstreamError(account *config.Account, err error, refreshed map[string]bool) upstreamAction {
	if account == nil || err == nil {
		return upstreamTryNextAccount
	}
	class := classifyUpstreamError(err)
	if class.isRequestScoped() {
		logger.Warnf("[AccountFailover] %s error is caused by the request; not retrying: %v", class, err)
		return upstreamStopRetrying
	}
	if class == upstreamErrTokenInvalid && !refreshed[account.ID] {
		refreshed[account.ID] = true
		ok, refreshErr := h.refreshAccountToken(account, true)
		if refreshErr == nil && ok {
			logger.Infof("[AccountFailover] Refreshed rejected token for %s; retrying same account", account.Email)
			return upstreamRetrySameAccount
		}
		if refreshErr != nil {
			logger.Warnf("[AccountFailover] Forced token refresh failed for %s: %v", account.Email, refreshErr)
		}
	}
	h.handleAccountFailure(account, err)
	return upstreamTryNextAccount
}
