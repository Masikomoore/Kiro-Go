package auth

import (
	"io"
	"kiro-go/config"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// proxyLoginRecorder records which client (proxy or global) served each request
// path, so tests can assert every login leg went through the chosen proxy.
type proxyLoginRecorder struct {
	mu     sync.Mutex
	routes map[string][]string // client name -> request paths
}

func (r *proxyLoginRecorder) record(client, path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.routes == nil {
		r.routes = map[string][]string{}
	}
	r.routes[client] = append(r.routes[client], path)
}

func (r *proxyLoginRecorder) count(client string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes[client])
}

func (r *proxyLoginRecorder) has(client, suffix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.routes[client] {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

type proxyLoginRoundTrip func(*http.Request) (*http.Response, error)

func (fn proxyLoginRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func proxyLoginJSON(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// proxyLoginClient builds a fake client named name whose transport records each
// request path and answers with handle.
func proxyLoginClient(rec *proxyLoginRecorder, name string, handle func(path string) *http.Response) *http.Client {
	return &http.Client{Transport: proxyLoginRoundTrip(func(req *http.Request) (*http.Response, error) {
		rec.record(name, req.URL.Path)
		return handle(req.URL.Path), nil
	})}
}

const addAccountTestProxy = "http://proxy.test:3128"

// installProxyLoginClients makes the global auth client fail the test path
// (it only counts) and installs the fake proxy client for addAccountTestProxy.
func installProxyLoginClients(t *testing.T, rec *proxyLoginRecorder, proxyHandle func(path string) *http.Response) {
	t.Helper()
	restoreGlobal := SetGlobalAuthClientForTest(proxyLoginClient(rec, "global", func(string) *http.Response {
		return proxyLoginJSON(500, `{"error":"global client must not be used"}`)
	}))
	t.Cleanup(func() { SetGlobalAuthClientForTest(restoreGlobal) })
	restoreProxy := SetAuthProxyClientForTest(addAccountTestProxy, proxyLoginClient(rec, "proxy", proxyHandle))
	t.Cleanup(restoreProxy)
}

func TestBuilderIdLoginUsesChosenProxyForRegisterDeviceAuthAndPoll(t *testing.T) {
	rec := &proxyLoginRecorder{}
	installProxyLoginClients(t, rec, func(path string) *http.Response {
		switch {
		case strings.HasSuffix(path, "/client/register"):
			return proxyLoginJSON(200, `{"clientId":"cid","clientSecret":"csecret"}`)
		case strings.HasSuffix(path, "/device_authorization"):
			return proxyLoginJSON(200, `{"deviceCode":"dc","userCode":"UC-1","verificationUri":"https://device.example","interval":1,"expiresIn":600}`)
		case strings.HasSuffix(path, "/token"):
			return proxyLoginJSON(400, `{"error":"authorization_pending"}`)
		}
		return proxyLoginJSON(404, `{}`)
	})

	session, err := StartBuilderIdLogin("us-east-1", addAccountTestProxy)
	if err != nil {
		t.Fatalf("StartBuilderIdLogin() error = %v", err)
	}
	if session.ProxyURL != addAccountTestProxy {
		t.Fatalf("session.ProxyURL = %q, want %q", session.ProxyURL, addAccountTestProxy)
	}
	if got := BuilderIdSessionProxyURL(session.ID); got != addAccountTestProxy {
		t.Fatalf("BuilderIdSessionProxyURL() = %q, want %q", got, addAccountTestProxy)
	}

	_, _, _, _, _, _, status, err := PollBuilderIdAuth(session.ID)
	if err != nil {
		t.Fatalf("PollBuilderIdAuth() error = %v", err)
	}
	if status != "pending" {
		t.Fatalf("PollBuilderIdAuth() status = %q, want pending", status)
	}

	if n := rec.count("global"); n != 0 {
		t.Fatalf("global auth client served %d request(s); every leg must use the proxy", n)
	}
	for _, suffix := range []string{"/client/register", "/device_authorization", "/token"} {
		if !rec.has("proxy", suffix) {
			t.Errorf("proxy client did not serve %s", suffix)
		}
	}
}

func TestBuilderIdLoginWithoutProxyUsesGlobalClient(t *testing.T) {
	rec := &proxyLoginRecorder{}
	restoreGlobal := SetGlobalAuthClientForTest(proxyLoginClient(rec, "global", func(path string) *http.Response {
		switch {
		case strings.HasSuffix(path, "/client/register"):
			return proxyLoginJSON(200, `{"clientId":"cid","clientSecret":"csecret"}`)
		case strings.HasSuffix(path, "/device_authorization"):
			return proxyLoginJSON(200, `{"deviceCode":"dc","userCode":"UC-1","interval":1,"expiresIn":600}`)
		}
		return proxyLoginJSON(404, `{}`)
	}))
	defer SetGlobalAuthClientForTest(restoreGlobal)

	session, err := StartBuilderIdLogin("us-east-1", "")
	if err != nil {
		t.Fatalf("StartBuilderIdLogin() error = %v", err)
	}
	if session.ProxyURL != "" {
		t.Fatalf("session.ProxyURL = %q, want empty", session.ProxyURL)
	}
	if n := rec.count("global"); n != 2 {
		t.Fatalf("global client served %d request(s), want 2", n)
	}
}

func TestIamSsoLoginUsesChosenProxyForRegisterAndTokenExchange(t *testing.T) {
	rec := &proxyLoginRecorder{}
	installProxyLoginClients(t, rec, func(path string) *http.Response {
		switch {
		case strings.HasSuffix(path, "/client/register"):
			return proxyLoginJSON(200, `{"clientId":"cid","clientSecret":"csecret"}`)
		case strings.HasSuffix(path, "/token"):
			return proxyLoginJSON(200, `{"accessToken":"at","refreshToken":"rt","expiresIn":3600}`)
		}
		return proxyLoginJSON(404, `{}`)
	})

	sessionID, _, _, err := StartIamSsoLogin("https://example.awsapps.com/start", "us-east-1", addAccountTestProxy)
	if err != nil {
		t.Fatalf("StartIamSsoLogin() error = %v", err)
	}
	if got := IamSsoSessionProxyURL(sessionID); got != addAccountTestProxy {
		t.Fatalf("IamSsoSessionProxyURL() = %q, want %q", got, addAccountTestProxy)
	}

	sessionsMu.RLock()
	state := sessions[sessionID].State
	sessionsMu.RUnlock()
	callback := "http://127.0.0.1/oauth/callback?code=abc&state=" + state
	if _, _, _, _, _, _, err := CompleteIamSsoLogin(sessionID, callback); err != nil {
		t.Fatalf("CompleteIamSsoLogin() error = %v", err)
	}

	if n := rec.count("global"); n != 0 {
		t.Fatalf("global auth client served %d request(s); IAM SSO must use the proxy", n)
	}
	if !rec.has("proxy", "/client/register") || !rec.has("proxy", "/token") {
		t.Fatalf("proxy client routes = %v, want register and token", rec.routes["proxy"])
	}
}

func TestMicrosoftSSOStartStoresChosenProxyOnlyWhenExplicit(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	explicit, _, _, err := StartMicrosoftSSOLogin(" " + addAccountTestProxy + " ")
	if err != nil {
		t.Fatalf("StartMicrosoftSSOLogin(explicit) error = %v", err)
	}
	defer CancelMicrosoftSSOLogin(explicit)
	session := getMicrosoftSSOSession(explicit)
	if session == nil {
		t.Fatal("explicit Microsoft session not found")
	}
	if session.ProxyURL != addAccountTestProxy || session.chosenProxyURL != addAccountTestProxy {
		t.Fatalf("explicit session proxies = (%q, %q), want %q", session.ProxyURL, session.chosenProxyURL, addAccountTestProxy)
	}

	defaulted, _, _, err := StartMicrosoftSSOLogin("")
	if err != nil {
		t.Fatalf("StartMicrosoftSSOLogin(default) error = %v", err)
	}
	defer CancelMicrosoftSSOLogin(defaulted)
	session = getMicrosoftSSOSession(defaulted)
	if session == nil {
		t.Fatal("default Microsoft session not found")
	}
	if session.chosenProxyURL != "" {
		t.Fatalf("default session chosenProxyURL = %q, want empty so the account stays on the global proxy", session.chosenProxyURL)
	}
}
