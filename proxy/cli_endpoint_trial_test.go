package proxy

import (
	"bytes"
	"context"
	"io"
	"kiro-go/config"
	"net/http"
	"strings"
	"testing"
)

// With compat.cliEndpointFirst, an OAuth account tries the CLI runtime first.
// A refusal there must fall back to the IDE endpoint, not fail the account.
func TestCLIEndpointTrialFallsBackToIDE(t *testing.T) {
	useCompat(t, config.CompatSettings{CLIEndpointFirst: true})
	var hosts []string
	installKiroStreamTestClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hosts = append(hosts, req.URL.Host)
		if strings.HasPrefix(req.URL.Host, "runtime.") {
			if got := req.Header.Get("Content-Type"); got != "application/x-amz-json-1.0" {
				t.Errorf("CLI trial content type = %q", got)
			}
			return &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"message":"not allowed"}`))}, nil
		}
		var frames bytes.Buffer
		frames.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "hello"}))
		frames.Write(awsEventStreamFrame(t, "meteringEvent", map[string]interface{}{"unit": "credit", "usage": 0.1}))
		return kiroStreamTestResponse(&frames), nil
	}))

	account := &config.Account{ID: "oauth-1", AuthMethod: "social", AccessToken: "tok", Region: "us-east-1",
		ProfileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/TEST", ProxyURL: kiroRetryTestProxyURL}
	var text strings.Builder
	err := CallKiroAPIContext(context.Background(), account, newKiroRetryTestPayload(), &KiroStreamCallback{
		OnText: func(s string, _ bool) { text.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("expected fallback success, got %v", err)
	}
	if len(hosts) < 2 || !strings.HasPrefix(hosts[0], "runtime.") || strings.HasPrefix(hosts[len(hosts)-1], "runtime.") {
		t.Fatalf("expected CLI first then an IDE host, got %v", hosts)
	}
	if text.String() != "hello" {
		t.Fatalf("unexpected text %q", text.String())
	}
}

func TestCLIEndpointFirstOffKeepsIDEOnly(t *testing.T) {
	useCompat(t, config.CompatSettings{})
	for _, ep := range endpointsForAccount(&config.Account{AuthMethod: "social"}) {
		if ep.Origin == kiroCLIEndpoint.Origin {
			t.Fatalf("CLI runtime must not be used for OAuth accounts by default: %+v", ep)
		}
	}
}

func TestCLITrialHeaderMarksOnlyThatRequest(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "/v1/messages", nil)
	if cliTrialRequested(withCLITrial(context.Background(), r)) {
		t.Fatal("no header must not request the CLI trial")
	}
	r.Header.Set("X-Kiro-Go-Endpoint", "cli")
	if !cliTrialRequested(withCLITrial(context.Background(), r)) {
		t.Fatal("header must request the CLI trial")
	}
}
