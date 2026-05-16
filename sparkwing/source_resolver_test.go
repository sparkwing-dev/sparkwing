package sparkwing_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestFactory_EnvSource_HitAndMiss(t *testing.T) {
	t.Setenv("SW_DEPLOY_TOKEN", "swu_real")
	t.Setenv("SW_EMPTY", "")
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "shell", Type: sources.TypeEnv, Prefix: "SW_"},
		nil)
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
	r, _ := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "shell", Type: sources.TypeEnv}, nil)
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
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "dot", Type: sources.TypeFile, Path: path}, nil)
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
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "dot", Type: sources.TypeFile, Path: filepath.Join(t.TempDir(), "absent.env")}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, _, err := r.Resolve(context.Background(), "ANY"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("absent file should resolve to ErrSecretMissing, got %v", err)
	}
}

func TestFactory_FileSource_RequiresPath(t *testing.T) {
	if _, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "dot", Type: sources.TypeFile}, nil); err == nil {
		t.Fatal("expected path-required error")
	}
}

func TestFactory_RemoteControllerSource(t *testing.T) {
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

	lookups := 0
	lookup := sparkwing.ProfileLookup(func(name string) (string, string, error) {
		lookups++
		if name != "shared" {
			t.Errorf("lookup called for %q, want shared", name)
		}
		return srv.URL, "testtoken", nil
	})
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "team-vault", Type: sources.TypeRemoteController, Controller: "shared"},
		lookup)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if lookups != 1 {
		t.Errorf("profile lookup called %d times, want 1", lookups)
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

func TestFactory_RemoteController_RequiresProfileLookup(t *testing.T) {
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "x", Type: sources.TypeRemoteController, Controller: "shared"}, nil)
	if err == nil || !strings.Contains(err.Error(), "profile lookup") {
		t.Fatalf("expected profile-lookup-required error, got %v", err)
	}
}

func TestFactory_RemoteController_PropagatesLookupError(t *testing.T) {
	bumpy := errors.New("profiles.yaml missing")
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "x", Type: sources.TypeRemoteController, Controller: "shared"},
		sparkwing.ProfileLookup(func(string) (string, string, error) { return "", "", bumpy }))
	if err == nil || !errors.Is(err, bumpy) {
		t.Fatalf("expected lookup error to propagate, got %v", err)
	}
}

func TestFactory_MacosKeychain_NonDarwinActionable(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin only")
	}
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "kc", Type: sources.TypeMacosKeychain, Service: "x"}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if _, _, rerr := r.Resolve(context.Background(), "anything"); rerr == nil || !strings.Contains(rerr.Error(), "darwin") {
		t.Errorf("expected darwin-only error, got %v", rerr)
	}
}

func TestFactory_UnknownTypeRejected(t *testing.T) {
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "x", Type: "vault-pro"}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("expected unknown-type error, got %v", err)
	}
}

func TestFactory_EmptyTypeRejected(t *testing.T) {
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(),
		sources.Source{Name: "x", Type: ""}, nil)
	if err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("expected type-required error, got %v", err)
	}
}
