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

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestApplySecretsProfileOverride_NormalReadsRemoteProfile(t *testing.T) {
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
    controller:
      url: %s
      token: t-stage
`, srv.URL)
	if err := os.WriteFile(filepath.Join(cfgDir, "profiles.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write profiles.yaml: %v", err)
	}
	t.Setenv("SPARKWING_SECRETS_PROFILE", "stage")

	var opts Options
	if err := applySecretsProfileOverride(&opts); err != nil {
		t.Fatalf("applySecretsProfileOverride: %v", err)
	}
	src := opts.SecretSource

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

func TestApplySecretsProfileOverride_LocalOnlyDoesNotOpenRemoteProfile(t *testing.T) {
	profiles := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(profiles, []byte(`profiles:
  dead:
    controller: { url: http://127.0.0.1:1 }
`), 0o600); err != nil {
		t.Fatalf("write profiles: %v", err)
	}
	t.Setenv("SPARKWING_PROFILES", profiles)
	t.Setenv("SPARKWING_SECRETS_PROFILE", "dead")

	opts := Options{LocalOnly: true}
	if err := applySecretsProfileOverride(&opts); err != nil {
		t.Fatalf("local-only override: %v", err)
	}
	if opts.SecretSource != nil {
		t.Fatalf("local-only run opened remote source %T", opts.SecretSource)
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
  only: {}
`
	if err := os.WriteFile(filepath.Join(tmpHome, ".config", "sparkwing", "profiles.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := remoteSecretSource("", ""); err == nil {
		t.Fatal("empty profile name must error")
	}
	if _, err := remoteSecretSource("ghost", ""); err == nil {
		t.Fatal("unknown profile must error")
	}
	if _, err := remoteSecretSource("only", ""); err == nil {
		t.Fatal("profile without controller must error")
	}
}
