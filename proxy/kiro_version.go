// Kiro IDE version discovery and self-healing refresh.
//
// Portions adapted from hank9999/kiro.rs (MIT): src/kiro/kiro_version.rs.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"kiro-go/config"
	"kiro-go/logger"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultKiroReleaseMetadataURL = "https://prod.download.desktop.kiro.dev/stable/metadata-linux-x64-stable.json"
	kiroVersionRefreshInterval    = 12 * time.Hour
	kiroVersionOnDemandCooldown   = time.Minute
	kiroVersionFetchTimeout       = 10 * time.Second
)

var (
	kiroReleaseMetadataURL = defaultKiroReleaseMetadataURL
	validKiroVersionRe     = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)

	kiroVersionMu          sync.Mutex
	lastKiroVersionRefresh time.Time
	lastOnDemandAttempt    time.Time
)

type kiroReleaseMetadata struct {
	CurrentRelease string `json:"currentRelease"`
}

func isUnsupportedKiroVersionBody(body string) bool {
	return strings.Contains(strings.ToLower(body), "version of kiro is no longer supported")
}

// FetchLatestKiroVersion queries the Kiro desktop release metadata endpoint and
// returns currentRelease.
func FetchLatestKiroVersion(ctx context.Context, proxyURL string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(ctx, kiroVersionFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, kiroReleaseMetadataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := GetRestClientForProxy(proxyURL).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from Kiro release metadata", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	var meta kiroReleaseMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", err
	}
	ver := strings.TrimSpace(meta.CurrentRelease)
	if !validKiroVersionRe.MatchString(ver) {
		return "", fmt.Errorf("invalid currentRelease %q in Kiro metadata", ver)
	}
	return ver, nil
}

// RefreshKiroVersion fetches the latest Kiro IDE version and updates the active
// in-memory version if newer.
func RefreshKiroVersion(ctx context.Context, proxyURL string) (string, error) {
	ver, err := FetchLatestKiroVersion(ctx, proxyURL)
	if err != nil {
		return "", err
	}
	if config.SetAutoKiroVersion(ver) {
		logger.Infof("[KiroVersion] Updated Kiro IDE version to %s (effective: %s)", ver, config.GetKiroClientConfig().KiroVersion)
	}
	return ver, nil
}

// RefreshKiroVersionOnStartup runs an initial non-blocking version check at server boot.
func RefreshKiroVersionOnStartup() {
	refreshKiroVersionIfStale(true)
}

func refreshKiroVersionIfStale(force bool) {
	kiroVersionMu.Lock()
	if !force && !lastKiroVersionRefresh.IsZero() && time.Since(lastKiroVersionRefresh) < kiroVersionRefreshInterval {
		kiroVersionMu.Unlock()
		return
	}
	lastKiroVersionRefresh = time.Now()
	kiroVersionMu.Unlock()

	if _, err := RefreshKiroVersion(context.Background(), config.GetProxyURL()); err != nil {
		logger.Warnf("[KiroVersion] Auto-discovery failed (continuing with %s): %v", config.GetKiroClientConfig().KiroVersion, err)
	}
}

// refreshKiroVersionOnDemand is triggered when Kiro rejects a request with
// "This version of Kiro is no longer supported". Rate-limited so a burst of
// concurrent requests shares one metadata check.
func refreshKiroVersionOnDemand(ctx context.Context) bool {
	kiroVersionMu.Lock()
	defer kiroVersionMu.Unlock()

	if !lastOnDemandAttempt.IsZero() && time.Since(lastOnDemandAttempt) < kiroVersionOnDemandCooldown {
		return config.GetAutoKiroVersion() != ""
	}
	lastOnDemandAttempt = time.Now()
	lastKiroVersionRefresh = lastOnDemandAttempt

	ver, err := RefreshKiroVersion(ctx, config.GetProxyURL())
	if err != nil {
		logger.Warnf("[KiroVersion] On-demand refresh after version rejection failed: %v", err)
		return false
	}
	return ver != ""
}
