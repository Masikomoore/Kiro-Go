package proxy

import (
	"context"
	"errors"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

const deadProxyTestURL = "test://dead-proxy"

func setUpstreamConnectTimeoutForTest(t *testing.T, d time.Duration) {
	t.Helper()
	old := upstreamConnectTimeout
	upstreamConnectTimeout = func() time.Duration { return d }
	t.Cleanup(func() { upstreamConnectTimeout = old })
}

// blackholeTransport never connects: it blocks until the attempt is cancelled.
func blackholeTransport(calls *atomic.Int32) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
}

func TestIsEgressFailureClassifiesRouteErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"connect breaker", errKiroConnect, true},
		{"wrapped connect breaker", &url.Error{Op: "Post", URL: "https://x", Err: errKiroConnect}, true},
		{"proxy refused CONNECT", &url.Error{Op: "Post", URL: "https://x", Err: &net.OpError{Op: "proxyconnect", Net: "tcp", Err: errors.New("refused")}}, true},
		{"socks handshake", errors.New("socks connect tcp 10.0.0.1:1080->q.us-east-1.amazonaws.com:443: refused"), true},
		{"dial timeout", &url.Error{Op: "Post", URL: "https://x", Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}}, true},
		{"stream stalled is upstream", errKiroStreamIdle, false},
		{"http body mentioning proxyconnect", newKiroHTTPErrorForTest("proxyconnect failed"), false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isEgressFailure(tc.err); got != tc.want {
			t.Errorf("%s: isEgressFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func newKiroHTTPErrorForTest(body string) error {
	return &kiroHTTPError{StatusCode: http.StatusInternalServerError, Endpoint: "test", Body: body}
}

func TestKiroConnectTimeoutFailsFastWithoutOtherEndpoints(t *testing.T) {
	installKiroRetryTestEndpoints(t)
	setUpstreamConnectTimeoutForTest(t, 40*time.Millisecond)
	var calls atomic.Int32
	installKiroStreamTestClient(t, blackholeTransport(&calls))

	start := time.Now()
	err := CallKiroAPIContext(context.Background(), newKiroRetryTestOAuthAccount(), newKiroRetryTestPayload(), &KiroStreamCallback{})
	elapsed := time.Since(start)

	if !errors.Is(err, errKiroConnect) {
		t.Fatalf("err = %v, want errKiroConnect", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("an unreachable proxy must not be retried on other endpoints or the same one, calls=%d", got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("connect timeout took %s, want about the 40ms limit", elapsed)
	}
}

func TestDirectAccountConnectFailureDoesNotOpenProxyBreaker(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	account := newKiroRetryTestOAuthAccount()
	account.ProxyURL = ""
	h := &Handler{pool: accountpool.GetPool()}
	if h.markProxyEgressFailure(account, errKiroConnect) {
		t.Fatal("a direct account (no proxy) must not open a proxy breaker")
	}
	if health := h.pool.ProxyHealthSnapshot(); len(health) != 0 {
		t.Fatalf("no breaker expected, got %+v", health)
	}
}

func TestProxyEgressFailureFailsOverToDirectAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeKiroSuccess(t, w, "from direct account")
	}))
	defer server.Close()
	setUpstreamConnectTimeoutForTest(t, 50*time.Millisecond)
	var deadCalls atomic.Int32
	oldClient, _ := proxyClientCache.Load(deadProxyTestURL)
	proxyClientCache.Store(deadProxyTestURL, &http.Client{Transport: blackholeTransport(&deadCalls)})
	t.Cleanup(func() {
		if oldClient != nil {
			proxyClientCache.Store(deadProxyTestURL, oldClient)
		} else {
			proxyClientCache.Delete(deadProxyTestURL)
		}
	})

	h := setupFailoverTest(t, server,
		config.Account{ID: "dead-proxy-a", AccessToken: "token-a", ProfileArn: "arn:aws:codewhisperer:profile/a", ProxyURL: deadProxyTestURL, Priority: 0},
		config.Account{ID: "dead-proxy-b", AccessToken: "token-b", ProfileArn: "arn:aws:codewhisperer:profile/b", Priority: 1},
	)
	t.Cleanup(func() { accountpool.GetPool().ClearProxyDown(deadProxyTestURL) })

	rec := postClaudeNonStream(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the direct account; body=%s", rec.Code, rec.Body.String())
	}
	if got := deadCalls.Load(); got != 1 {
		t.Fatalf("first request should try the dead proxy once, calls=%d", got)
	}
	if health := accountpool.GetPool().ProxyHealthSnapshot(); len(health) == 0 || health[0].URL != deadProxyTestURL {
		t.Fatalf("dead proxy breaker not open: %+v", health)
	}

	// The next request must not touch the dead proxy at all.
	if rec := postClaudeNonStream(t, h); rec.Code != http.StatusOK {
		t.Fatalf("second status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := deadCalls.Load(); got != 1 {
		t.Fatalf("breaker open: second request must skip the dead proxy, calls=%d", got)
	}
}

func TestRecoveryProbeClearsProxyBreakerWhenProxyAnswers(t *testing.T) {
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxySrv.Close()
	oldTarget := proxyHealthProbeTarget
	proxyHealthProbeTarget = "http://kiro.invalid/"
	t.Cleanup(func() { proxyHealthProbeTarget = oldTarget })

	p := accountpool.GetPool()
	p.MarkProxyDown(proxySrv.URL, time.Now().Add(time.Hour), "earlier failure")
	t.Cleanup(func() { p.ClearProxyDown(proxySrv.URL) })

	h := &Handler{pool: p, stopRefresh: make(chan struct{})}
	h.probeDownProxies()
	if health := p.ProxyHealthSnapshot(); len(health) != 0 {
		t.Fatalf("a probe that gets an answer must close the breaker, still open: %+v", health)
	}
}

func TestRecoveryProbeExtendsProxyBreakerWhileProxyIsDead(t *testing.T) {
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := proxySrv.URL
	proxySrv.Close()
	oldTarget := proxyHealthProbeTarget
	proxyHealthProbeTarget = "http://kiro.invalid/"
	t.Cleanup(func() { proxyHealthProbeTarget = oldTarget })

	p := accountpool.GetPool()
	p.MarkProxyDown(deadURL, time.Now().Add(time.Second), "earlier failure")
	t.Cleanup(func() { p.ClearProxyDown(deadURL) })

	h := &Handler{pool: p, stopRefresh: make(chan struct{})}
	h.probeDownProxies()
	health := p.ProxyHealthSnapshot()
	if len(health) != 1 {
		t.Fatalf("a failing probe must keep the breaker open, got %+v", health)
	}
	if left := time.Until(health[0].Until); left < time.Minute {
		t.Fatalf("failed probe should extend the breaker by the proxy-down cooldown, %s left", left)
	}
}

// The two tests below use the real transport, not a fake: they check that a
// proxy that accepts but never answers, or refuses, is seen as an egress
// failure and that the account is handed back without more attempts.
func TestSilentProxyFailsOverAsEgressFailure(t *testing.T) {
	installKiroRetryTestEndpoints(t)
	setUpstreamConnectTimeoutForTest(t, 300*time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	account := newKiroRetryTestOAuthAccount()
	account.ProxyURL = "http://" + ln.Addr().String()

	err = CallKiroAPIContext(context.Background(), account, newKiroRetryTestPayload(), &KiroStreamCallback{})
	if !errors.Is(err, errKiroConnect) || !isEgressFailure(err) {
		t.Fatalf("silent proxy: err = %v, want an egress failure", err)
	}
}

func TestRefusedProxyFailsOverAsEgressFailure(t *testing.T) {
	installKiroRetryTestEndpoints(t)
	setUpstreamConnectTimeoutForTest(t, 5*time.Second)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	account := newKiroRetryTestOAuthAccount()
	account.ProxyURL = "http://" + addr

	err = CallKiroAPIContext(context.Background(), account, newKiroRetryTestPayload(), &KiroStreamCallback{})
	if err == nil || !isEgressFailure(err) {
		t.Fatalf("refused proxy: err = %v, want an egress failure", err)
	}
}
