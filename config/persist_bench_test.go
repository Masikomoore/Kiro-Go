package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// seedBenchAccounts fills the config with n realistic accounts and one API key.
func seedBenchAccounts(b *testing.B, n int) string {
	b.Helper()
	if err := Init(filepath.Join(b.TempDir(), "config.json")); err != nil {
		b.Fatalf("Init: %v", err)
	}
	cfgLock.Lock()
	for i := 0; i < n; i++ {
		cfg.Accounts = append(cfg.Accounts, Account{
			ID:           fmt.Sprintf("acc-%04d", i),
			Email:        fmt.Sprintf("user%04d@example.com", i),
			AccessToken:  "aoa" + strings.Repeat("x", 1200),
			RefreshToken: "aor" + strings.Repeat("y", 900),
			ClientID:     strings.Repeat("c", 80),
			ClientSecret: strings.Repeat("s", 900),
			AuthMethod:   "idc",
			Provider:     "BuilderId",
			Region:       "us-east-1",
			MachineId:    strings.Repeat("m", 64),
			Enabled:      true,
		})
	}
	cfgLock.Unlock()
	entry, err := AddApiKey(ApiKeyEntry{Name: "bench", Key: "sk-bench"})
	if err != nil {
		b.Fatalf("AddApiKey: %v", err)
	}
	return entry.ID
}

func BenchmarkRecordApiKeyUsage(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("accounts=%d", n), func(b *testing.B) {
			id := seedBenchAccounts(b, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := RecordApiKeyUsage(id, 1000, 0.1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkUpdateAccountStats(b *testing.B) {
	for _, n := range []int{100, 500} {
		b.Run(fmt.Sprintf("accounts=%d", n), func(b *testing.B) {
			seedBenchAccounts(b, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := UpdateAccountStats("acc-0001", i, 0, i*10, 0, 0); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
