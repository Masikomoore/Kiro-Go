package proxy

import (
	"context"
	"io"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func resetKiroVersionStateForTest(t *testing.T) {
	t.Helper()
	prevAuto := config.GetAutoKiroVersion()
	prevURL := kiroReleaseMetadataURL
	t.Cleanup(func() {
		config.SetAutoKiroVersion(prevAuto)
		kiroReleaseMetadataURL = prevURL
		kiroVersionMu.Lock()
		lastKiroVersionRefresh = time.Time{}
		lastOnDemandAttempt = time.Time{}
		kiroVersionMu.Unlock()
	})
	kiroVersionMu.Lock()
	lastKiroVersionRefresh = time.Time{}
	lastOnDemandAttempt = time.Time{}
	kiroVersionMu.Unlock()
}

func TestCompareVersionStrings(t *testing.T) {
	if config.CompareVersionStrings("0.11.107", "1.4.154") >= 0 {
		t.Fatalf("expected 0.11.107 < 1.4.154")
	}
	if config.CompareVersionStrings("1.4.154", "1.4.154") != 0 {
		t.Fatalf("expected 1.4.154 == 1.4.154")
	}
	if config.CompareVersionStrings("1.5.0", "1.4.154") <= 0 {
		t.Fatalf("expected 1.5.0 > 1.4.154")
	}
}

func TestRefreshKiroVersionUpdatesClientConfig(t *testing.T) {
	resetKiroVersionStateForTest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"currentRelease":"9.8.7","releases":[]}`))
	}))
	defer srv.Close()
	kiroReleaseMetadataURL = srv.URL

	ver, err := RefreshKiroVersion(context.Background(), "")
	if err != nil {
		t.Fatalf("RefreshKiroVersion failed: %v", err)
	}
	if ver != "9.8.7" {
		t.Fatalf("expected 9.8.7, got %q", ver)
	}
	if got := config.GetKiroClientConfig().KiroVersion; got != "9.8.7" {
		t.Fatalf("expected effective KiroVersion 9.8.7, got %q", got)
	}
}

func TestCallKiroAPIRefreshesVersionAndRetriesOnUnsupportedVersionError(t *testing.T) {
	resetKiroVersionStateForTest(t)
	config.SetAutoKiroVersion(config.DefaultKiroVersion)

	metaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"currentRelease":"9.9.99"}`))
	}))
	defer metaSrv.Close()
	kiroReleaseMetadataURL = metaSrv.URL

	installKiroRetryTestEndpoints(t)
	var seenUAs []string
	installKiroStreamTestClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		ua := req.Header.Get("User-Agent")
		seenUAs = append(seenUAs, ua)
		if !strings.Contains(ua, "KiroIDE-9.9.99") {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"message":"This version of Kiro is no longer supported. Please update to the latest version of Kiro to continue."}`)),
				Header:     make(http.Header),
			}, nil
		}
		var body strings.Builder
		body.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{"content": "ok"}))
		body.Write(awsEventStreamFrame(t, "metadataEvent", map[string]interface{}{"stopReason": "end_turn"}))
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body.String())),
			Header:     make(http.Header),
		}, nil
	}))

	var out strings.Builder
	err := CallKiroAPI(newKiroRetryTestOAuthAccount(), newKiroRetryTestPayload(), &KiroStreamCallback{
		OnText: func(text string, isThinking bool) {
			if !isThinking {
				out.WriteString(text)
			}
		},
	})
	if err != nil {
		t.Fatalf("expected retry after version refresh to succeed, got %v", err)
	}
	if out.String() != "ok" {
		t.Fatalf("expected output 'ok', got %q", out.String())
	}
	if len(seenUAs) != 2 {
		t.Fatalf("expected 2 upstream attempts (initial + refreshed retry), got %d: %v", len(seenUAs), seenUAs)
	}
	if !strings.Contains(seenUAs[1], "KiroIDE-9.9.99") {
		t.Fatalf("expected second attempt to carry KiroIDE-9.9.99, got %q", seenUAs[1])
	}
}
