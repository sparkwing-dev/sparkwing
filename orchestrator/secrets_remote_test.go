package orchestrator

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/secrets"
)

// wing --secrets PROF wires through to remoteSecretSource which
// reads the profile from
// ~/.config/sparkwing/profiles.yaml and builds an HTTP-backed
// secrets.Source. We verify both the happy path and the
// "name not found" -> ErrSecretMissing translation.

func TestRemoteSecretSource_ResolvesAgainstProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/secrets/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/secrets/")
		switch name {
		case "TOKEN":
			fmt.Fprintln(w, `{"name":"TOKEN","value":"prod-abc","principal":"admin","masked":true,"created_at":1,"updated_at":1}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	cfgDir := filepath.Join(tmpHome, ".config", "sparkwing")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := fmt.Sprintf(`default: stage
profiles:
  stage:
    controller: %s
    token: t-stage
`, srv.URL)
	if err := os.WriteFile(filepath.Join(cfgDir, "profiles.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write profiles.yaml: %v", err)
	}

	src, err := remoteSecretSource("stage")
	if err != nil {
		t.Fatalf("remoteSecretSource: %v", err)
	}

	got, masked, err := src.Read("TOKEN")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "prod-abc" || !masked {
		t.Fatalf("Read = (%q, masked=%v), want (prod-abc, true)", got, masked)
	}

	_, _, err = src.Read("MISSING")
	if !errors.Is(err, secrets.ErrSecretMissing) {
		t.Fatalf("Read missing: err = %v, want ErrSecretMissing", err)
	}
}

func TestRemoteSecretSource_BadProfileErrors(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	if err := os.MkdirAll(filepath.Join(tmpHome, ".config", "sparkwing"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := `default: only
profiles:
  only:
    controller: ""
    token: ""
`
	if err := os.WriteFile(filepath.Join(tmpHome, ".config", "sparkwing", "profiles.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := remoteSecretSource(""); err == nil {
		t.Fatal("empty profile name must error")
	}
	if _, err := remoteSecretSource("ghost"); err == nil {
		t.Fatal("unknown profile must error")
	}
	if _, err := remoteSecretSource("only"); err == nil {
		t.Fatal("profile without controller must error")
	}
}
