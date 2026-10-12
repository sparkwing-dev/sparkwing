package storeurl_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func TestOpenArtifactStoreFromSpec_Filesystem(t *testing.T) {
	dir := t.TempDir()
	store, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: dir}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestOpenLogStoreFromSpec_Filesystem(t *testing.T) {
	dir := t.TempDir()
	store, err := storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: dir}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestOpenFromSpec_UnrecognizedType(t *testing.T) {
	ctx := context.Background()
	for _, ty := range []string{"nope", "gcs", "azure-blob", "mysql"} {
		spec := backends.Spec{Type: ty, Bucket: "x", URL: "x"}
		_, cacheErr := storeurl.OpenArtifactStoreFromSpec(ctx, spec, nil)
		_, logsErr := storeurl.OpenLogStoreFromSpec(ctx, spec, nil)
		_, stateErr := storeurl.OpenStateStoreFromSpec(ctx, spec, nil)
		for _, tc := range []struct {
			err  error
			want string
		}{
			{cacheErr, "(use filesystem, s3 or controller)"},
			{logsErr, "(use filesystem, s3, stdout or controller)"},
			{stateErr, "(use sqlite or controller)"},
		} {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Errorf("type %q: err = %v, want it to name %s", ty, tc.err, tc.want)
			}
		}
	}
}

func TestOpenLogStoreFromSpec_Stdout(t *testing.T) {
	store, err := storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeStdout}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestOpenLogStoreFromSpec_StdoutRejectsExtraFields(t *testing.T) {
	_, err := storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeStdout, Bucket: "foo"}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "does not accept configuration fields") {
		t.Errorf("want 'does not accept configuration fields', got: %v", err)
	}
}

func TestOpenArtifactStoreFromSpec_FilesystemMissingPath(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "path is required") {
		t.Errorf("want 'path is required', got: %v", err)
	}
}

func TestOpenStateStoreFromSpec_SQLite(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"
	st, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeSQLite, Path: path}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if st == nil {
		t.Fatal("nil store")
	}
	defer func() { _ = st.Close() }()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected db file at %s: %v", path, err)
	}
}

func TestOpenStateStoreFromSpec_SQLiteMissingPath(t *testing.T) {
	_, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeSQLite}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "path is required") {
		t.Errorf("want 'path is required', got: %v", err)
	}
}

func TestOpenStateStoreFromSpec_Controller(t *testing.T) {
	lookup := func(name string) (string, string, error) {
		if name != "prod" {
			t.Fatalf("lookup called with %q, want prod", name)
		}
		return "https://controller.example", "tok-123", nil
	}
	st, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "prod"}, lookup)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if st == nil {
		t.Fatal("nil store")
	}
	defer func() { _ = st.Close() }()
}

func TestOpenStateStoreFromSpec_ControllerRequiresLookup(t *testing.T) {
	_, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "prod"}, nil)
	if err == nil {
		t.Fatal("expected error when lookup is nil")
	}
	if !strings.Contains(err.Error(), "profile lookup") {
		t.Errorf("want 'profile lookup', got: %v", err)
	}
}

func TestOpenStateStoreFromSpec_UnrecognizedType(t *testing.T) {
	_, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem, Path: "/tmp/x"}, nil)
	if err == nil || !strings.Contains(err.Error(), "(use sqlite or controller)") {
		t.Errorf("want the supported state types, got: %v", err)
	}
}

func TestOpenArtifactStoreFromSpec_Controller(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("payload"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	lookup := func(name string) (string, string, error) {
		if name != "shared" {
			return "", "", fmt.Errorf("unknown profile %q", name)
		}
		return srv.URL, "tok-123", nil
	}
	store, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "shared"}, lookup)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put(context.Background(), "k", strings.NewReader("data")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if sawAuth != "Bearer tok-123" {
		t.Errorf("authorization header = %q, want Bearer tok-123", sawAuth)
	}
	rc, err := store.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "payload" {
		t.Errorf("get body = %q", string(got))
	}
}

func TestOpenLogStoreFromSpec_Controller(t *testing.T) {
	var sawAuth, sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, sawPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte("logged\n"))
	}))
	defer srv.Close()

	lookup := func(name string) (string, string, error) {
		return srv.URL, "tok-xyz", nil
	}
	store, err := storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "shared"}, lookup)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := store.Read(context.Background(), "r", "n", storage.ReadOpts{})
	if err != nil || string(got) != "logged\n" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if sawAuth != "Bearer tok-xyz" || sawPath != "/api/v1/runs/r/logs/n" {
		t.Errorf("request = %s with %q, want the controller's node log route with Bearer tok-xyz", sawPath, sawAuth)
	}
	if err := store.Append(context.Background(), "r", "n", []byte("hi\n")); !errors.Is(err, storage.ErrNotSupported) {
		t.Fatalf("append = %v, want refused: writers upload segments", err)
	}
}

func TestOpenFromSpec_ControllerMissingProfileField(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController}, func(string) (string, string, error) {
			return "x", "", nil
		})
	if err == nil || !strings.Contains(err.Error(), "requires controller: <profile-name>") {
		t.Errorf("cache: want missing controller error, got %v", err)
	}
	_, err = storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController}, func(string) (string, string, error) {
			return "x", "", nil
		})
	if err == nil || !strings.Contains(err.Error(), "requires controller: <profile-name>") {
		t.Errorf("logs: want missing controller error, got %v", err)
	}
}

func TestOpenFromSpec_ControllerLookupNil(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "shared"}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a profile lookup") {
		t.Errorf("cache: want missing lookup error, got %v", err)
	}
	_, err = storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "shared"}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a profile lookup") {
		t.Errorf("logs: want missing lookup error, got %v", err)
	}
}

func TestOpenFromSpec_ControllerLookupError(t *testing.T) {
	lookup := func(string) (string, string, error) {
		return "", "", fmt.Errorf("profile not found")
	}
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "missing"}, lookup)
	if err == nil || !strings.Contains(err.Error(), "profile not found") {
		t.Errorf("want propagated profile error, got %v", err)
	}
}

var _ = errors.New
