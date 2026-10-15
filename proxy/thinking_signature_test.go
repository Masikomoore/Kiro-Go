package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func thinkingUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "reasoningContentEvent", map[string]interface{}{"text": "let me think"}))
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "final answer"}))
		_, _ = w.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
	}))
}

const thinkingRequestBody = `{
	"model":"claude-sonnet-4.5",
	"max_tokens":4000,
	"thinking":{"type":"enabled","budget_tokens":2000},
	"messages":[{"role":"user","content":"hello"}]%s
}`

func TestClaudeStreamThinkingBlockCarriesSignatureBeforeStop(t *testing.T) {
	server := thinkingUpstream(t)
	defer server.Close()
	h := setupIntegrityPathTest(t, server)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Replace(thinkingRequestBody, "%s", `,"stream":true`, 1)))
	rec := httptest.NewRecorder()
	h.handleClaudeMessages(rec, req)
	body := rec.Body.String()

	sigAt := strings.Index(body, `"signature_delta"`)
	if sigAt < 0 {
		t.Fatalf("thinking block must carry a signature_delta, got %s", body)
	}
	thinkingStop := strings.Index(body[sigAt:], `"type":"content_block_stop"`)
	if thinkingStop < 0 {
		t.Fatalf("signature_delta must precede the thinking block stop, got %s", body)
	}
	if !strings.Contains(body, thinkingSignaturePlaceholder) {
		t.Fatalf("expected the placeholder signature, got %s", body)
	}
	if strings.Count(body, `"signature_delta"`) != 1 {
		t.Fatalf("only the thinking block gets a signature, got %s", body)
	}
}

func TestClaudeNonStreamThinkingBlockCarriesSignature(t *testing.T) {
	server := thinkingUpstream(t)
	defer server.Close()
	h := setupIntegrityPathTest(t, server)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(strings.Replace(thinkingRequestBody, "%s", "", 1)))
	rec := httptest.NewRecorder()
	h.handleClaudeMessages(rec, req)

	var resp ClaudeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Type != "thinking" {
		t.Fatalf("expected a leading thinking block, got %+v", resp.Content)
	}
	if resp.Content[0].Signature != thinkingSignaturePlaceholder {
		t.Fatalf("thinking block signature = %q", resp.Content[0].Signature)
	}
	for _, block := range resp.Content[1:] {
		if block.Signature != "" {
			t.Fatalf("non-thinking block got a signature: %+v", block)
		}
	}
}
