package sparkwing_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestNewSecretResolverFromSource_RemoteController(t *testing.T) {
	const wantToken = "factory-test-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/secrets/")
		name, _ = url.PathUnescape(name)
		switch name {
		case "GH_TOKEN":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": "ghp_xyz", "masked": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	src := sources.Source{Name: "team", Type: sources.TypeRemoteController, Controller: "shared"}
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(), src,
		func(_ string) (string, string, error) { return srv.URL, wantToken, nil })
	if err != nil {
		t.Fatalf("build resolver: %v", err)
	}
	val, masked, err := r.Resolve(context.Background(), "GH_TOKEN")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if val != "ghp_xyz" || !masked {
		t.Errorf("got (%q, %v), want (ghp_xyz, true)", val, masked)
	}

	if _, _, err := r.Resolve(context.Background(), "MISSING"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("MISSING: got %v, want ErrSecretMissing", err)
	}
}

func TestNewSecretResolverFromSource_RemoteController_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	src := sources.Source{Name: "team", Type: sources.TypeRemoteController, Controller: "shared"}
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(), src,
		func(_ string) (string, string, error) { return srv.URL, "bad", nil })
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, _, err = r.Resolve(context.Background(), "ANY")
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("got %v", err)
	}
}

func TestNewSecretResolverFromSource_RemoteController_RequiresProfileLookup(t *testing.T) {
	src := sources.Source{Name: "team", Type: sources.TypeRemoteController, Controller: "shared"}
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(), src, nil)
	if err == nil || !strings.Contains(err.Error(), "profile lookup") {
		t.Errorf("got %v", err)
	}
}

func TestNewSecretResolverFromSource_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secrets.env")
	body := "TOKEN=abc123\n# comment\nQUOTED=\"with spaces\"\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	src := sources.Source{Name: "dotenv", Type: sources.TypeFile, Path: path}
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(), src, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cases := []struct {
		name      string
		want      string
		wantErr   error
		wantValue bool
	}{
		{"TOKEN", "abc123", nil, true},
		{"QUOTED", "with spaces", nil, true},
		{"EMPTY", "", nil, true},
		{"MISSING", "", sparkwing.ErrSecretMissing, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, masked, err := r.Resolve(context.Background(), tc.name)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Errorf("got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("got err %v", err)
			}
			if val != tc.want {
				t.Errorf("value = %q, want %q", val, tc.want)
			}
			if !masked {
				t.Errorf("file values should be masked=true")
			}
		})
	}
}

func TestNewSecretResolverFromSource_File_MissingPath(t *testing.T) {
	src := sources.Source{Name: "dotenv", Type: sources.TypeFile}
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(), src, nil)
	if err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Errorf("got %v", err)
	}
}

func TestNewSecretResolverFromSource_File_MissingFileIsEmpty(t *testing.T) {
	src := sources.Source{Name: "dotenv", Type: sources.TypeFile, Path: filepath.Join(t.TempDir(), "absent")}
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(), src, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, _, err = r.Resolve(context.Background(), "ANY")
	if !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("got %v, want ErrSecretMissing", err)
	}
}

func TestNewSecretResolverFromSource_Env(t *testing.T) {
	t.Setenv("SW_GH_TOKEN", "from-env")
	src := sources.Source{Name: "shell", Type: sources.TypeEnv, Prefix: "SW_"}
	r, err := sparkwing.NewSecretResolverFromSource(context.Background(), src, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	val, _, err := r.Resolve(context.Background(), "GH_TOKEN")
	if err != nil || val != "from-env" {
		t.Errorf("got (%q, %v), want (from-env, nil)", val, err)
	}
	if _, _, err := r.Resolve(context.Background(), "MISSING"); !errors.Is(err, sparkwing.ErrSecretMissing) {
		t.Errorf("MISSING: got %v", err)
	}
}

func TestNewSecretResolverFromSource_UnknownType(t *testing.T) {
	src := sources.Source{Name: "what", Type: "unknown"}
	_, err := sparkwing.NewSecretResolverFromSource(context.Background(), src, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("got %v", err)
	}
}
