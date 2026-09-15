package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOpenControllerStoreDefaultsToSQLite(t *testing.T) {
	unsetenv(t, controllerPostgresEnv)

	path := filepath.Join(t.TempDir(), "state.db")
	st, err := openControllerStore(context.Background(), path)
	if err != nil {
		t.Fatalf("open controller store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got := st.Dialect(); got != store.DialectSQLite {
		t.Fatalf("dialect = %v, want sqlite", got)
	}
}

func TestOpenControllerStoreUsesConfiguredPostgres(t *testing.T) {
	t.Setenv(controllerPostgresEnv, "")
	_, err := openControllerStore(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err == nil || !strings.Contains(err.Error(), controllerPostgresEnv+" is empty or unset") {
		t.Fatalf("open controller store error = %v, want configured Postgres source refusal", err)
	}
}

func unsetenv(t *testing.T, name string) {
	t.Helper()
	old, existed := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			if err := os.Setenv(name, old); err != nil {
				t.Errorf("restore %s: %v", name, err)
			}
			return
		}
		if err := os.Unsetenv(name); err != nil {
			t.Errorf("clear %s: %v", name, err)
		}
	})
}
