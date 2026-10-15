package proxy

import (
	"net/http"

	"kiro-go/logger"
)

// mapUpstreamError converts the final upstream error of a request into the
// client-facing response and logs the full error under the same request ID.
// The client never sees the raw upstream body.
func (h *Handler) mapUpstreamError(model string, err error) clientErrorResponse {
	resp := upstreamClientError(err, h.pool.NextAvailableIn(model))
	logger.Warnf("[UpstreamError] class=%s request_id=%s status=%d model=%s: %v",
		classifyUpstreamError(err), resp.RequestID, resp.Status, model, err)
	return resp
}

// writeClaudeUpstreamFailure answers a non-streaming or not-yet-started
// Anthropic request whose account attempts all failed.
func (h *Handler) writeClaudeUpstreamFailure(w http.ResponseWriter, endpoint, model string, lastErr error) {
	if lastErr == nil {
		h.sendClaudeError(w, http.StatusServiceUnavailable, "api_error", "No available accounts")
		return
	}
	h.recordFailureWithDetails(endpoint, model, "", lastErr)
	resp := h.mapUpstreamError(model, lastErr)
	setRetryAfterHeader(w, resp.RetryAfter)
	h.sendClaudeError(w, resp.Status, resp.Type, resp.Message)
}

// writeOpenAIUpstreamFailure is the OpenAI-format counterpart.
func (h *Handler) writeOpenAIUpstreamFailure(w http.ResponseWriter, endpoint, model string, lastErr error) {
	if lastErr == nil {
		h.sendOpenAIError(w, http.StatusServiceUnavailable, "server_error", "No available accounts")
		return
	}
	h.recordFailureWithDetails(endpoint, model, "", lastErr)
	resp := h.mapUpstreamError(model, lastErr)
	setRetryAfterHeader(w, resp.RetryAfter)
	h.sendOpenAIError(w, resp.Status, openAIErrorType(resp.Status), resp.Message)
}

func openAIErrorType(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status >= 400 && status < 500:
		return "invalid_request_error"
	default:
		return "server_error"
	}
}
