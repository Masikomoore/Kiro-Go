package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"kiro-go/config"
	"net/http"
	"strings"
	"testing"
)

func setPlaceholderArnMode(t *testing.T, mode string) {
	t.Helper()
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.UpdateCompatSettings(config.CompatSettings{BuilderIDPlaceholderArn: mode}); err != nil {
		t.Fatalf("update compat: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })
}

// stubBuilderIDProfileLookup makes ListAvailableProfiles answer like it does
// for Builder ID, so profile resolution fails softly without network access.
func stubBuilderIDProfileLookup(t *testing.T) {
	t.Helper()
	clearProfileArnResolutionCooldowns()
	t.Cleanup(clearProfileArnResolutionCooldowns)
	kiroRestHttpStore.Store(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader(`{"message":"AWS Builder ID is not supported for this operation.","reason":null}`)),
			Header:     make(http.Header),
		}, nil
	})})
	t.Cleanup(func() { InitKiroHttpClient("") })
}

func builderIDTestAccount() *config.Account {
	acc := newKiroRetryTestOAuthAccount()
	acc.ProfileArn = ""
	acc.Provider = "BuilderId"
	acc.AuthMethod = "idc"
	acc.ID = "builder-placeholder"
	return acc
}

func recordProfileArns(t *testing.T, requireArn bool) *[]string {
	t.Helper()
	seen := &[]string{}
	installKiroStreamTestClient(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		var sent struct {
			ProfileArn string `json:"profileArn"`
		}
		_ = json.Unmarshal(body, &sent)
		*seen = append(*seen, sent.ProfileArn)
		if requireArn && sent.ProfileArn == "" {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"message":"profileArn is required for this request."}`)),
				Header:     make(http.Header),
			}, nil
		}
		return kiroStreamTestResponse(bytes.NewReader(awsEventStreamFrame(t,
			"assistantResponseEvent", map[string]interface{}{"content": "ok"}))), nil
	}))
	return seen
}

func TestPlaceholderArnOnDemandRetriesOnce(t *testing.T) {
	setPlaceholderArnMode(t, config.PlaceholderArnOnDemand)
	stubBuilderIDProfileLookup(t)
	installKiroRetryTestEndpoints(t)
	seen := recordProfileArns(t, true)

	payload := newKiroRetryTestPayload()
	if err := CallKiroAPI(builderIDTestAccount(), payload, &KiroStreamCallback{}); err != nil {
		t.Fatalf("CallKiroAPI: %v", err)
	}
	if got := strings.Join(*seen, ","); got != ","+builderIDPlaceholderProfileArn {
		t.Fatalf("profileArn sequence = %q, want empty then placeholder", got)
	}
	if payload.ProfileArn != "" {
		t.Fatalf("payload must be restored, got %q", payload.ProfileArn)
	}
}

func TestPlaceholderArnAlwaysAndOff(t *testing.T) {
	setPlaceholderArnMode(t, config.PlaceholderArnAlways)
	installKiroRetryTestEndpoints(t)
	seen := recordProfileArns(t, false)
	if err := CallKiroAPI(builderIDTestAccount(), newKiroRetryTestPayload(), &KiroStreamCallback{}); err != nil {
		t.Fatalf("CallKiroAPI: %v", err)
	}
	if got := strings.Join(*seen, ","); got != builderIDPlaceholderProfileArn {
		t.Fatalf("always mode must send the placeholder first, got %q", got)
	}

	_ = config.UpdateCompatSettings(config.CompatSettings{})
	stubBuilderIDProfileLookup(t)
	seen = recordProfileArns(t, true)
	err := CallKiroAPI(builderIDTestAccount(), newKiroRetryTestPayload(), &KiroStreamCallback{})
	if classifyUpstreamError(err) != upstreamErrProfileRequired || len(*seen) != 1 {
		t.Fatalf("off mode must not retry: err=%v calls=%v", err, *seen)
	}
}

func TestPlaceholderProfileArnEligibility(t *testing.T) {
	if got := placeholderProfileArn(&config.Account{AuthMethod: "social"}); got != socialPlaceholderProfileArn {
		t.Fatalf("social = %q", got)
	}
	if got := placeholderProfileArn(&config.Account{AuthMethod: "idc", Provider: "Enterprise"}); got != "" {
		t.Fatalf("enterprise accounts must not get a placeholder, got %q", got)
	}
	if got := placeholderProfileArn(&config.Account{AuthMethod: "api_key", KiroApiKey: "ksk"}); got != "" {
		t.Fatalf("API key accounts must not get a placeholder, got %q", got)
	}
}
