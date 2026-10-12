package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLiteDSN_BusyTimeout(t *testing.T) {
	t.Setenv("SPARKWING_SQLITE_BUSY_TIMEOUT_MS", "12345")
	for _, dsn := range []string{sqliteDSN("state.db"), sqliteReadOnlyDSN("state.db")} {
		if !strings.Contains(dsn, "busy_timeout(30000)") {
			t.Errorf("DSN %q, want the default busy_timeout; the environment is no longer read", dsn)
		}
	}
	SetTestBusyTimeout(t, 1500)
	for _, dsn := range []string{sqliteDSN("state.db"), sqliteReadOnlyDSN("state.db")} {
		if !strings.Contains(dsn, "busy_timeout(1500)") {
			t.Errorf("DSN %q missing the test busy_timeout", dsn)
		}
	}
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open with a test busy timeout: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
