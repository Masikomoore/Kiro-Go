package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// proxyCheckTarget is the Kiro chat host. Any HTTP response from it, whatever
// the status, means the proxy reaches Kiro.
const proxyCheckTarget = "https://q.us-east-1.amazonaws.com/"

const proxyCheckTimeout = 15 * time.Second

type proxyCheckResult struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Target    string `json:"target"`
	Error     string `json:"error,omitempty"`
}

// validateProxyURL accepts an empty URL (direct) or an http, https, socks5 or
// socks5h URL with a host.
func validateProxyURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid proxy URL: %v", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return fmt.Errorf("proxyURL must start with http://, https://, socks5://, or socks5h://")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("proxy URL has no host")
	}
	return nil
}

// checkProxy sends one request to target through proxyURL on a fresh
// connection. An empty proxyURL checks the direct route that requests take
// without a proxy.
func checkProxy(ctx context.Context, proxyURL, target string) proxyCheckResult {
	result := proxyCheckResult{Target: target}
	transport := buildKiroTransport(proxyURL)
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   proxyCheckTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	start := time.Now()
	resp, err := client.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = redactProxyUserinfo(err.Error(), proxyURL)
		return result
	}
	resp.Body.Close()
	result.OK = true
	result.Status = resp.StatusCode
	return result
}

// redactProxyUserinfo keeps proxy credentials out of an error shown in the UI.
func redactProxyUserinfo(msg, proxyURL string) string {
	u, err := url.Parse(proxyURL)
	if err != nil || u.User == nil {
		return msg
	}
	if info := u.User.String(); info != "" {
		msg = strings.ReplaceAll(msg, info, "***")
	}
	if pass, ok := u.User.Password(); ok && pass != "" {
		msg = strings.ReplaceAll(msg, pass, "***")
	}
	return msg
}
