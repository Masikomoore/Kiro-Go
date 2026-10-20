package proxy

import (
	"context"
	"encoding/json"
	"kiro-go/config"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckProxyReachesTargetThroughProxy(t *testing.T) {
	var seenHost string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost = r.URL.Host
		w.WriteHeader(http.StatusForbidden)
	}))
	defer proxySrv.Close()

	got := checkProxy(context.Background(), proxySrv.URL, "http://kiro.invalid/")
	if !got.OK || got.Status != http.StatusForbidden {
		t.Fatalf("any HTTP response through the proxy means it connects, got %+v", got)
	}
	if seenHost != "kiro.invalid" {
		t.Fatalf("request did not go through the proxy, proxy saw host %q", seenHost)
	}
}

func TestCheckProxyReportsUnreachableProxyWithoutCredentials(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	got := checkProxy(context.Background(), "http://user:s3cret@"+addr, "http://kiro.invalid/")
	if got.OK || got.Error == "" {
		t.Fatalf("closed proxy port must fail, got %+v", got)
	}
	if strings.Contains(got.Error, "s3cret") {
		t.Fatalf("error leaks the proxy password: %s", got.Error)
	}
}

func TestSavedProxiesRoundTrip(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	h := &Handler{}

	rec := httptest.NewRecorder()
	h.apiUpdateSavedProxies(rec, httptest.NewRequest(http.MethodPost, "/proxy/saved",
		strings.NewReader(`{"proxies":[{"name":" hk ","url":"socks5://1.2.3.4:1080"},{"name":"us","url":"http://u:p@5.6.7.8:3128"}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.apiGetProxy(rec, httptest.NewRequest(http.MethodGet, "/proxy", nil))
	var got struct {
		ProxyURL string              `json:"proxyURL"`
		Proxies  []config.SavedProxy `json:"proxies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Proxies) != 2 || got.Proxies[0].Name != "hk" || got.Proxies[1].URL != "http://u:p@5.6.7.8:3128" {
		t.Fatalf("saved proxies = %+v", got.Proxies)
	}
	if got.ProxyURL != "" {
		t.Fatalf("saving the list must not change the active proxy, got %q", got.ProxyURL)
	}

	for _, body := range []string{
		`{"proxies":[{"name":"a","url":"ftp://1.2.3.4:21"}]}`,
		`{"proxies":[{"name":"a","url":"http://1.2.3.4:1"},{"name":"a","url":"http://1.2.3.4:2"}]}`,
		`{"proxies":[{"name":"","url":"http://1.2.3.4:1"}]}`,
	} {
		rec = httptest.NewRecorder()
		h.apiUpdateSavedProxies(rec, httptest.NewRequest(http.MethodPost, "/proxy/saved", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", body, rec.Code)
		}
	}
	if n := len(config.GetSavedProxies()); n != 2 {
		t.Fatalf("a rejected update must not change the list, have %d", n)
	}
}
