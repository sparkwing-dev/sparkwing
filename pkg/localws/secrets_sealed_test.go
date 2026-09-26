package localws

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestLocalwsSealsTheSecretsItStores(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: starts the local server; the fast class runs under -short")
	}
	t.Setenv(localsecrets.KeyFileEnv, filepath.Join(t.TempDir(), "secrets.key"))
	t.Setenv(localsecrets.KeyEnv, "")
	home := t.TempDir()
	addr := startLocalws(t, Options{Home: home})

	resp, err := http.Post("http://"+addr+"/api/v1/secrets", "application/json",
		strings.NewReader(`{"name":"TOKEN","value":"abc123","shared":true}`))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("create secret status = %d, want 204", resp.StatusCode)
	}

	paths, err := localPaths(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenReadOnly(paths.StateDB())
	if err != nil {
		t.Fatalf("open the local state db: %v", err)
	}
	defer func() { _ = st.Close() }()
	sec, err := st.GetSecretRow("TOKEN", "")
	if err != nil {
		t.Fatalf("read TOKEN: %v", err)
	}
	if !secrets.IsBound(sec.Value) || strings.Contains(sec.Value, "abc123") {
		t.Fatalf("stored value %q is not a sealed envelope", sec.Value)
	}
}
