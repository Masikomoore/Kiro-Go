package proxy

import (
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reorderTestHandler starts a fresh config file with three enabled accounts (a, b, c)
// and a fourth unlisted account (d) whose priority must survive a reorder.
func reorderTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	for i, id := range []string{"a", "b", "c", "d"} {
		acc := config.Account{ID: id, Email: id + "@example.com", RefreshToken: "rt-" + id, Enabled: true}
		if id == "d" {
			acc.Priority = 5
		}
		if err := config.AddAccount(acc); err != nil {
			t.Fatalf("AddAccount %d: %v", i, err)
		}
	}
	return &Handler{pool: accountpool.GetPool()}, cfgFile
}

func postReorder(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/api/accounts/reorder", strings.NewReader(body))
	req.Header.Set("X-Admin-Password", config.GetPassword())
	rec := httptest.NewRecorder()
	h.handleAdminAPI(rec, req)
	return rec
}

func priorityByID(t *testing.T, id string) int {
	t.Helper()
	for _, a := range config.GetAccounts() {
		if a.ID == id {
			return a.Priority
		}
	}
	t.Fatalf("account %s missing", id)
	return 0
}

func TestReorderAccountsAssignsRanksPersistsAndReachesPool(t *testing.T) {
	h, cfgFile := reorderTestHandler(t)

	rec := postReorder(t, h, `{"ids":["c","a","b"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	want := map[string]int{"c": 1, "a": 2, "b": 3, "d": 5}
	for id, p := range want {
		if got := priorityByID(t, id); got != p {
			t.Fatalf("priority[%s] = %d, want %d", id, got, p)
		}
	}
	if got := accountpool.GetPool().GetByID("c"); got == nil || got.Priority != 1 {
		t.Fatalf("pool account c = %+v, want priority 1", got)
	}

	// Reload from disk to prove the change was saved, not just held in memory.
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	for id, p := range want {
		if got := priorityByID(t, id); got != p {
			t.Fatalf("persisted priority[%s] = %d, want %d", id, got, p)
		}
	}
}

func TestReorderAccountsUnknownIDIsBadRequestAndChangesNothing(t *testing.T) {
	h, _ := reorderTestHandler(t)

	rec := postReorder(t, h, `{"ids":["a","missing"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if got := priorityByID(t, "a"); got != 0 {
		t.Fatalf("priority[a] = %d after rejected request, want 0", got)
	}
}

func TestReorderAccountsDuplicateIDIsBadRequestAndChangesNothing(t *testing.T) {
	h, _ := reorderTestHandler(t)

	rec := postReorder(t, h, `{"ids":["a","b","a"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if got := priorityByID(t, "a"); got != 0 {
		t.Fatalf("priority[a] = %d after rejected request, want 0", got)
	}
	if got := priorityByID(t, "b"); got != 0 {
		t.Fatalf("priority[b] = %d after rejected request, want 0", got)
	}
}

func TestReorderAccountsEmptyListIsBadRequest(t *testing.T) {
	h, _ := reorderTestHandler(t)

	rec := postReorder(t, h, `{"ids":[]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
