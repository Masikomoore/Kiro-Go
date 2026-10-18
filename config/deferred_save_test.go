package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readSavedApiKey(t *testing.T, path, id string) ApiKeyEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var saved Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	for _, k := range saved.ApiKeys {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("key %s not in saved config", id)
	return ApiKeyEntry{}
}

func TestRecordApiKeyUsageDefersWriteUntilFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatalf("Init: %v", err)
	}
	entry, err := AddApiKey(ApiKeyEntry{Name: "deferred", Key: "sk-deferred"})
	if err != nil {
		t.Fatalf("AddApiKey: %v", err)
	}

	if err := RecordApiKeyUsage(entry.ID, 100, 0.5); err != nil {
		t.Fatalf("RecordApiKeyUsage: %v", err)
	}
	if got := GetApiKeyEntry(entry.ID); got == nil || got.TokensUsed != 100 || got.RequestsCount != 1 {
		t.Fatalf("in-memory usage not updated: %+v", got)
	}
	if saved := readSavedApiKey(t, path, entry.ID); saved.RequestsCount != 0 {
		t.Fatalf("usage reached disk on the request path: %+v", saved)
	}

	if err := FlushPendingSave(); err != nil {
		t.Fatalf("FlushPendingSave: %v", err)
	}
	if saved := readSavedApiKey(t, path, entry.ID); saved.TokensUsed != 100 || saved.RequestsCount != 1 {
		t.Fatalf("flush did not persist usage: %+v", saved)
	}
}

func TestSaveCoversDeferredChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Init(path); err != nil {
		t.Fatalf("Init: %v", err)
	}
	entry, err := AddApiKey(ApiKeyEntry{Name: "deferred", Key: "sk-deferred-2"})
	if err != nil {
		t.Fatalf("AddApiKey: %v", err)
	}
	if err := RecordApiKeyUsage(entry.ID, 7, 0); err != nil {
		t.Fatalf("RecordApiKeyUsage: %v", err)
	}
	// Any later full save writes the pending counters and clears the flag.
	if err := UpdateApiKey(entry.ID, ApiKeyEntry{Name: "renamed"}); err != nil {
		t.Fatalf("UpdateApiKey: %v", err)
	}
	if saveDirty.Load() {
		t.Fatal("dirty flag survived a full save")
	}
	if saved := readSavedApiKey(t, path, entry.ID); saved.TokensUsed != 7 {
		t.Fatalf("full save dropped deferred usage: %+v", saved)
	}
}
