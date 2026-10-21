package proxy

import (
	"encoding/json"
	"kiro-go/config"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func postGroupOrder(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/api/accounts/group-order", strings.NewReader(body))
	req.Header.Set("X-Admin-Password", config.GetPassword())
	rec := httptest.NewRecorder()
	h.handleAdminAPI(rec, req)
	return rec
}

func getProxyConfig(t *testing.T, h *Handler) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("GET", "/admin/api/proxy", nil)
	req.Header.Set("X-Admin-Password", config.GetPassword())
	rec := httptest.NewRecorder()
	h.handleAdminAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /proxy status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode GET /proxy: %v", err)
	}
	return out
}

func TestGroupOrderPersistsAndIsReturnedByGetProxy(t *testing.T) {
	h, cfgFile := reorderTestHandler(t)
	order := []string{"socks5://user:secret@10.0.0.1:1080", "", "http://10.0.0.2:8080"}

	rec := postGroupOrder(t, h, `{"order":["socks5://user:secret@10.0.0.1:1080"," ","http://10.0.0.2:8080"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := config.GetGroupOrder(); !reflect.DeepEqual(got, []string{"socks5://user:secret@10.0.0.1:1080", "", "http://10.0.0.2:8080"}) {
		t.Fatalf("GetGroupOrder() = %q", got)
	}

	// Reload from disk to prove the order was saved, not just held in memory.
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := config.GetGroupOrder(); !reflect.DeepEqual(got, order) {
		t.Fatalf("persisted GetGroupOrder() = %q, want %q", got, order)
	}

	body := getProxyConfig(t, h)
	raw, ok := body["groupOrder"].([]interface{})
	if !ok || len(raw) != len(order) {
		t.Fatalf("GET /proxy groupOrder = %v, want %q", body["groupOrder"], order)
	}
	for i, want := range order {
		if raw[i] != want {
			t.Fatalf("GET /proxy groupOrder[%d] = %v, want %q", i, raw[i], want)
		}
	}
}

func TestGroupOrderDuplicateKeyIsBadRequestAndChangesNothing(t *testing.T) {
	h, _ := reorderTestHandler(t)
	if err := config.UpdateGroupOrder([]string{"http://10.0.0.2:8080"}); err != nil {
		t.Fatalf("UpdateGroupOrder: %v", err)
	}

	rec := postGroupOrder(t, h, `{"order":["","http://10.0.0.2:8080", " "]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if got := config.GetGroupOrder(); !reflect.DeepEqual(got, []string{"http://10.0.0.2:8080"}) {
		t.Fatalf("GetGroupOrder() = %q after rejected request, want unchanged", got)
	}
}

func TestGroupOrderInvalidJSONIsBadRequest(t *testing.T) {
	h, _ := reorderTestHandler(t)

	rec := postGroupOrder(t, h, `{"order":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
