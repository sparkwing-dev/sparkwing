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

func TestOpenArtifactStoreFromSpec_Unimplemented(t *testing.T) {
	cases := []string{backends.TypeGCS, backends.TypeAzureBlob}
	for _, ty := range cases {
		t.Run(ty, func(t *testing.T) {
			_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
				backends.Spec{Type: ty, Bucket: "x"}, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "not implemented in this build") {
				t.Errorf("want 'not implemented in this build', got: %v", err)
			}
		})
	}
}

func TestOpenLogStoreFromSpec_Unimplemented(t *testing.T) {
	for _, ty := range []string{backends.TypeGCS, backends.TypeAzureBlob} {
		t.Run(ty, func(t *testing.T) {
			_, err := storeurl.OpenLogStoreFromSpec(context.Background(),
				backends.Spec{Type: ty, Bucket: "x"}, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "not implemented in this build") {
				t.Errorf("want 'not implemented in this build', got: %v", err)
			}
		})
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

func TestOpenFromSpec_UnrecognizedType(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: "nope"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Errorf("want 'not recognized', got: %v", err)
	}
	_, err = storeurl.OpenLogStoreFromSpec(context.Background(),
		backends.Spec{Type: "nope"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Errorf("want 'not recognized', got: %v", err)
	}
}

func TestOpenArtifactStoreFromSpec_FilesystemMissingPath(t *testing.T) {
	_, err := storeurl.OpenArtifactStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeFilesystem}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	// not validated again here; pkg/backends.Validate catches it.
	// The factory still surfaces it through expandPath.
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

func TestOpenStateStoreFromSpec_Unimplemented(t *testing.T) {
	for _, ty := range []string{backends.TypePostgres, backends.TypeMySQL} {
		t.Run(ty, func(t *testing.T) {
			_, err := storeurl.OpenStateStoreFromSpec(context.Background(),
				backends.Spec{Type: ty, URL: "x"}, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "not implemented in this build") {
				t.Errorf("want 'not implemented in this build', got: %v", err)
			}
		})
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
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Errorf("want 'not recognized', got: %v", err)
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
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("logged\n"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
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
	if err := store.Append(context.Background(), "r", "n", []byte("hi\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if sawAuth != "Bearer tok-xyz" {
		t.Errorf("authorization header = %q, want Bearer tok-xyz", sawAuth)
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

// sanity: errors.New baseline so import is non-trivial in case the
// test file is trimmed in the future.
var _ = errors.New
