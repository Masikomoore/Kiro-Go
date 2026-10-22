package proxy

import (
	"context"
	"fmt"
	"kiro-go/config"
	"kiro-go/logger"
	"kiro-go/pool"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// proxyCheckTarget is the Kiro chat host. Any HTTP response from it, whatever
// the status, means the proxy reaches Kiro.
const proxyCheckTarget = "https://q.us-east-1.amazonaws.com/"

const proxyCheckTimeout = 15 * time.Second

// proxyHealthProbeInterval is how often each open proxy breaker is probed.
// Variables so tests can shorten the interval and point the probe elsewhere.
var (
	proxyHealthProbeInterval = 30 * time.Second
	proxyHealthProbeTarget   = proxyCheckTarget
)

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

// proxyHealthLoop probes every open proxy breaker until the handler stops. A
// successful probe closes the breaker at once; a failed one extends it, so a
// proxy that stays dead keeps its accounts skipped.
func (h *Handler) proxyHealthLoop() {
	ticker := time.NewTicker(proxyHealthProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.probeDownProxies()
		case <-h.stopRefresh:
			return
		}
	}
}

func (h *Handler) probeDownProxies() {
	for _, down := range h.pool.ProxyHealthSnapshot() {
		res := checkProxy(context.Background(), down.URL, proxyHealthProbeTarget)
		if res.OK {
			if cleared, ok := h.pool.ClearProxyDown(down.URL); ok {
				logger.Infof("[ProxyHealth] %s recovered after %s", proxyDisplayName(down.URL), time.Since(cleared.Since).Round(time.Second))
			}
			continue
		}
		until := time.Now().Add(config.GetCompatConfig().ProxyDownCooldown)
		h.pool.MarkProxyDown(down.URL, until, redactProxyUserinfo(res.Error, down.URL))
	}
}

// proxyHealthForAdmin describes the open proxy breakers for the admin panel.
// url is the raw proxy URL, the same string GET /proxy and GET /accounts use,
// so the panel can match it; name is the saved proxy name when there is one.
func proxyHealthForAdmin(entries []pool.ProxyHealth) []map[string]interface{} {
	saved := make(map[string]string)
	for _, p := range config.GetSavedProxies() {
		saved[p.URL] = p.Name
	}
	out := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]interface{}{
			"url":       e.URL,
			"name":      saved[e.URL],
			"down":      true,
			"since":     e.Since.Format(time.RFC3339),
			"until":     e.Until.Format(time.RFC3339),
			"lastError": e.LastError,
		})
	}
	return out
}
