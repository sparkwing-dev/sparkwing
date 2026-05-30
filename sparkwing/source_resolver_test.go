package sparkwing_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFactory_EnvSource_HitAndMiss(t *testing.T) {
	t.Setenv("SW_DEPLOY_TOKEN", "swu_real")
	t.Setenv("SW_EMPTY", "")
	r, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeEnv, Prefix: "SW_"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	v, masked, err := r.Resolve(context.Background(), "DEPLOY_TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "swu_real" || !masked {
		t.Errorf("got %q masked=%v", v, masked)
	}
	if _, _, err := r.Resolve(context.Background(), "ABSENT"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("missing env should be ErrSecretMissing, got %v", err)
	}
	if _, _, err := r.Resolve(context.Background(), "EMPTY"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("empty env should be ErrSecretMissing, got %v", err)
	}
}

func TestFactory_EnvSource_NoPrefix(t *testing.T) {
	t.Setenv("ABSOLUTE_NAME", "val")
	r, _ := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeEnv})
	v, _, err := r.Resolve(context.Background(), "ABSOLUTE_NAME")
	if err != nil || v != "val" {
		t.Errorf("Resolve = %q err=%v", v, err)
	}
}

func TestFactory_FileSource_ReadsDotenv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.env")
	if err := os.WriteFile(path, []byte("FOO=bar\n# comment\nBAZ=\"quoted\"\nEMPTY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: path})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	cases := map[string]string{"FOO": "bar", "BAZ": "quoted", "EMPTY": ""}
	for name, want := range cases {
		v, _, err := r.Resolve(context.Background(), name)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if v != want {
			t.Errorf("%s = %q, want %q", name, v, want)
		}
	}
	if _, _, err := r.Resolve(context.Background(), "MISSING"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("expected ErrSecretMissing, got %v", err)
	}
}

func TestFactory_FileSource_MissingFileTreatedAsEmpty(t *testing.T) {
	r, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: filepath.Join(t.TempDir(), "absent.env")})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, _, err := r.Resolve(context.Background(), "ANY"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("absent file should resolve to ErrSecretMissing, got %v", err)
	}
}

func TestFactory_FileSource_RequiresPath(t *testing.T) {
	if _, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem}); err == nil {
		t.Fatal("expected path-required error")
	}
}

func TestFactory_ControllerSource(t *testing.T) {
	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Header.Get("Authorization") != "Bearer testtoken" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Path, "/api/v1/secrets/") {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/MISSING") {
			http.Error(w, "absent", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": "vault-secret", "masked": true})
	}))
	defer srv.Close()

	r, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, URL: srv.URL, Token: "testtoken"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	v, masked, err := r.Resolve(context.Background(), "DEPLOY_TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if v != "vault-secret" || !masked {
		t.Errorf("got %q masked=%v", v, masked)
	}
	if _, _, err := r.Resolve(context.Background(), "MISSING"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("expected ErrSecretMissing for 404, got %v", err)
	}
	if called < 2 {
		t.Errorf("server hit %d times, want at least 2", called)
	}
}

func TestFactory_ControllerSource_TokenFromEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer envtoken" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": "ok", "masked": true})
	}))
	defer srv.Close()
	t.Setenv("SWTEST_CTRL_TOKEN", "envtoken")
	r, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, URL: srv.URL, TokenEnv: "SWTEST_CTRL_TOKEN"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	v, _, err := r.Resolve(context.Background(), "X")
	if err != nil || v != "ok" {
		t.Errorf("Resolve: %v %q", err, v)
	}
}

func TestFactory_ControllerSource_RequiresURL(t *testing.T) {
	_, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController})
	if err == nil || !strings.Contains(err.Error(), "url is empty") {
		t.Fatalf("expected url-required error, got %v", err)
	}
}

func TestFactory_UnknownTypeRejected(t *testing.T) {
	_, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: "vault-pro"})
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("expected unsupported-type error, got %v", err)
	}
}

func TestFactory_EmptyTypeRejected(t *testing.T) {
	_, err := sparkwing.NewSecretResolverFromSpec(context.Background(),
		backends.Spec{Type: ""})
	if err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("expected type-required error, got %v", err)
	}
}
