package proxy

import (
	"encoding/json"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminSettingsRoundTripCompat(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })
	h := &Handler{}

	rec := httptest.NewRecorder()
	h.apiUpdateSettings(rec, httptest.NewRequest(http.MethodPost, "/settings",
		strings.NewReader(`{"compat":{"promptCacheMode":"read_only","structuredToolHistory":true,"toolChunkingHints":false}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.apiGetSettings(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	var got struct {
		Compat    config.CompatSettings  `json:"compat"`
		Effective map[string]interface{} `json:"compatEffective"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Compat.PromptCacheMode != "read_only" || !got.Compat.StructuredToolHistory {
		t.Fatalf("stored compat = %+v", got.Compat)
	}
	if got.Effective["toolChunkingHints"] != false || got.Effective["nativeReasoningEffort"] != true ||
		got.Effective["accountThrottleCooldownMinutes"] != float64(10) {
		t.Fatalf("effective compat = %v", got.Effective)
	}

	rec = httptest.NewRecorder()
	h.apiUpdateSettings(rec, httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(`{"compat":{"promptCacheMode":"sometimes"}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid compat must be rejected, status %d", rec.Code)
	}
	if config.GetCompatConfig().PromptCacheMode != config.PromptCacheModeReadOnly {
		t.Fatal("a rejected update must not change settings")
	}
}

func TestAdminCacheSwitchesPreserveOtherCompat(t *testing.T) {
	if err := config.Init(t.TempDir() + "/config.json"); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	t.Cleanup(func() { _ = config.UpdateCompatSettings(config.CompatSettings{}) })
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.apiUpdateSettings(rec, httptest.NewRequest(http.MethodPost, "/settings",
		strings.NewReader(`{"compat":{"promptCacheMode":"read_only","structuredToolHistory":true,"simulateAnthropicCache":false,"simulateOpenAICache":true}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.apiGetSettings(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	var got struct {
		Compat    config.CompatSettings  `json:"compat"`
		Effective map[string]interface{} `json:"compatEffective"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Compat.PromptCacheMode != "read_only" || !got.Compat.StructuredToolHistory {
		t.Fatalf("other compat fields changed: %+v", got.Compat)
	}
	if got.Effective["simulateAnthropicCache"] != false || got.Effective["simulateOpenAICache"] != true || got.Effective["promptCacheMode"] != "read_only" {
		t.Fatalf("effective = %v", got.Effective)
	}
}
