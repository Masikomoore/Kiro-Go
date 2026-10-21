package proxy

import (
	"encoding/json"
	"io"
	"kiro-go/auth"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const addAccountProxyTestURL = "http://proxy.test:3128"

type addAccountProxyRoundTrip func(*http.Request) (*http.Response, error)

func (fn addAccountProxyRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

// addAccountProxyFakeKiro answers the refresh and usage-limit calls made while
// importing a credential. It records which named client served each request.
func addAccountProxyFakeKiro(mu *sync.Mutex, served map[string]int, name string) *http.Client {
	return &http.Client{Transport: addAccountProxyRoundTrip(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		served[name]++
		mu.Unlock()
		body := `{"error":"unexpected"}`
		status := http.StatusNotFound
		switch {
		case strings.HasSuffix(req.URL.Host, "amazonaws.com") && req.URL.Path == "/token":
			body, status = `{"accessToken":"new-at","refreshToken":"new-rt","expiresIn":3600}`, http.StatusOK
		case strings.Contains(req.URL.Path, "getUsageLimits"):
			body, status = `{"userInfo":{"email":"user@example.com","userId":"uid-1"}}`, http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
}

func addAccountProxyImport(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]int) {
	t.Helper()
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	var mu sync.Mutex
	served := map[string]int{}
	restoreGlobal := auth.SetGlobalAuthClientForTest(addAccountProxyFakeKiro(&mu, served, "global"))
	t.Cleanup(func() { auth.SetGlobalAuthClientForTest(restoreGlobal) })
	restoreProxy := auth.SetAuthProxyClientForTest(addAccountProxyTestURL, addAccountProxyFakeKiro(&mu, served, "proxy"))
	t.Cleanup(restoreProxy)

	h := &Handler{pool: accountpool.GetPool()}
	req := httptest.NewRequest("POST", "/auth/credentials", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.apiImportCredentials(rec, req)
	return rec, served
}

func addAccountProxySavedAccount(t *testing.T) config.Account {
	t.Helper()
	accounts := config.GetAccounts()
	if len(accounts) != 1 {
		t.Fatalf("saved accounts = %d, want 1", len(accounts))
	}
	return accounts[0]
}

func TestApiImportCredentialsWithProxyRefreshesThroughProxyAndStoresIt(t *testing.T) {
	body := `{"refreshToken":"rt-old","clientId":"c","clientSecret":"s","authMethod":"idc","region":"us-east-1","proxyURL":"  ` + addAccountProxyTestURL + `  "}`
	rec, served := addAccountProxyImport(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if served["global"] != 0 {
		t.Fatalf("global client served %d request(s); the import must go through the proxy", served["global"])
	}
	if served["proxy"] < 2 {
		t.Fatalf("proxy client served %d request(s), want refresh and usage calls", served["proxy"])
	}
	if got := addAccountProxySavedAccount(t).ProxyURL; got != addAccountProxyTestURL {
		t.Fatalf("saved ProxyURL = %q, want %q", got, addAccountProxyTestURL)
	}
}

func TestApiImportCredentialsWithoutProxyKeepsAccountOnGlobalProxy(t *testing.T) {
	body := `{"refreshToken":"rt-old","clientId":"c","clientSecret":"s","authMethod":"idc","region":"us-east-1"}`
	rec, served := addAccountProxyImport(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if served["proxy"] != 0 {
		t.Fatalf("proxy client served %d request(s) without a proxy choice", served["proxy"])
	}
	if served["global"] == 0 {
		t.Fatal("global client was not used for an import without a proxy choice")
	}
	if got := addAccountProxySavedAccount(t).ProxyURL; got != "" {
		t.Fatalf("saved ProxyURL = %q, want empty", got)
	}
}

func TestApiImportCredentialsRejectsInvalidProxyURL(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	h := &Handler{pool: accountpool.GetPool()}
	body := `{"refreshToken":"rt-old","clientId":"c","clientSecret":"s","authMethod":"idc","proxyURL":"ftp://proxy.test:21"}`
	rec := httptest.NewRecorder()
	h.apiImportCredentials(rec, httptest.NewRequest("POST", "/auth/credentials", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(resp["error"], "proxyURL must start with") {
		t.Fatalf("error = %q, want proxy scheme message", resp["error"])
	}
	if n := len(config.GetAccounts()); n != 0 {
		t.Fatalf("saved accounts = %d after rejected proxy, want 0", n)
	}
}

func TestApiStartBuilderIdLoginRejectsInvalidProxyURL(t *testing.T) {
	h := &Handler{pool: accountpool.GetPool()}
	rec := httptest.NewRecorder()
	h.apiStartBuilderIdLogin(rec, httptest.NewRequest("POST", "/auth/builderid/start", strings.NewReader(`{"proxyURL":"ftp://x"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "proxyURL must start with") {
		t.Fatalf("body = %s, want proxy scheme message", rec.Body.String())
	}
}
