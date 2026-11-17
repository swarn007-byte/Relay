package storage

import (
	"strings"
	"testing"
)

func TestRemoteConfigurationFailsClosed(t *testing.T) {
	for _, tc := range []struct{ url, token string }{
		{"", "secret-that-must-not-appear"},
		{"libsql://example.turso.io", ""},
		{"http://example.turso.io", "secret-that-must-not-appear"},
		{"https://user:secret-that-must-not-appear@example.turso.io", "token"},
		{"https://example.turso.io?authToken=secret-that-must-not-appear", "token"},
	} {
		db, err := OpenRemote(tc.url, tc.token)
		if db != nil {
			db.Close()
			t.Fatal("unexpected connection")
		}
		if err == nil {
			t.Fatal("incomplete or insecure configuration accepted")
		}
		if strings.Contains(err.Error(), "secret-that-must-not-appear") {
			t.Fatal("credentials exposed in error")
		}
	}
	t.Setenv("TURSO_DATABASE_URL", "libsql://example.turso.io")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	if db, err := OpenConfigured(t.TempDir() + "/fallback.db"); err == nil {
		db.Close()
		t.Fatal("must not silently fall back to local storage")
	}
}
