package proxy

import (
	"io"
	"kiro-go/auth"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A Builder ID account without a profile must not pay for an OIDC refresh on
// every request once the profile lookup has been found unsupported.
func TestResolveProfileArnDoesNotRefreshEveryRequestWhenUnsupported(t *testing.T) {
	mustInitConfig(t)
	clearProfileArnResolutionCooldowns()
	t.Cleanup(clearProfileArnResolutionCooldowns)

	var profileCalls int32
	kiroRestHttpStore.Store(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&profileCalls, 1)
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader(`{"message":"AWS Builder ID is not supported for this operation.","reason":null}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	t.Cleanup(func() { InitKiroHttpClient("") })

	var refreshCalls int32
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}`))
	}))
	t.Cleanup(authServer.Close)
	oldTokenURL := auth.GetOIDCTokenURLForTest()
	auth.SetOIDCTokenURLForTest(func(string) string { return authServer.URL })
	t.Cleanup(func() { auth.SetOIDCTokenURLForTest(oldTokenURL) })
	oldAuthClient := auth.SetGlobalAuthClientForTest(authServer.Client())
	t.Cleanup(func() { auth.SetGlobalAuthClientForTest(oldAuthClient) })

	account := &config.Account{
		ID:           "builder-hotpath-1",
		Email:        "builder@example.com",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		AuthMethod:   "idc",
		Provider:     "BuilderId",
		Region:       "us-east-1",
	}

	for i := 0; i < 5; i++ {
		if _, err := ResolveProfileArn(account); !isProfileArnResolutionSoftError(err) {
			t.Fatalf("call %d: expected soft error, got %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&refreshCalls); got > 1 {
		t.Fatalf("expected at most 1 OIDC refresh across 5 requests, got %d", got)
	}
	if got := atomic.LoadInt32(&profileCalls); got != 1 {
		t.Fatalf("expected 1 profile lookup, got %d", got)
	}
}

// A failed lookup that is not the Builder ID "unsupported" case is cached for
// a short window and returns the same hard error, so REST callers still fail
// while generation requests stop re-probing every region on each call.
func TestResolveProfileArnCachesLookupFailure(t *testing.T) {
	mustInitConfig(t)
	clearProfileArnResolutionCooldowns()
	t.Cleanup(clearProfileArnResolutionCooldowns)

	var profileCalls int32
	kiroRestHttpStore.Store(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&profileCalls, 1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"profiles":[]}`)),
				Header:     make(http.Header),
			}, nil
		}),
	})
	t.Cleanup(func() { InitKiroHttpClient("") })

	account := &config.Account{
		ID:          "idc-empty-1",
		Email:       "idc@example.com",
		AccessToken: "access-token",
		AuthMethod:  "idc",
		Region:      "us-east-1",
	}
	for i := 0; i < 3; i++ {
		_, err := ResolveProfileArn(account)
		if err == nil || !strings.Contains(err.Error(), "no available Kiro profile") || isProfileArnResolutionSoftError(err) {
			t.Fatalf("call %d: expected hard no-profile error, got %v", i, err)
		}
	}
	first := atomic.LoadInt32(&profileCalls)
	if first == 0 {
		t.Fatal("expected the first call to probe profiles")
	}

	// Expire the cooldown: the next call probes again.
	key := profileArnCooldownKey(account)
	profileArnResolutionCooldowns.Store(key, profileArnCooldown{until: time.Now().Add(-time.Second)})
	_, _ = ResolveProfileArn(account)
	if got := atomic.LoadInt32(&profileCalls); got != first*2 {
		t.Fatalf("expected probes only on the first call and after expiry: first=%d total=%d", first, got)
	}
}
