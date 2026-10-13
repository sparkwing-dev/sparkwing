package storeurl_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func TestOpenFromSpec_UnrecognizedType(t *testing.T) {
	ctx := context.Background()
	for _, ty := range []string{"nope", "gcs", "azure-blob", "mysql"} {
		spec := backends.Spec{Type: ty, Bucket: "x", URL: "x"}
		_, err := storeurl.OpenStateStoreFromSpec(ctx, spec, nil)
		if err == nil || !strings.Contains(err.Error(), "(use sqlite or controller)") {
			t.Errorf("type %q: err = %v, want it to name sqlite and controller", ty, err)
		}
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

func TestOpenFromSpec_ControllerLookupNil(t *testing.T) {
	_, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "shared"}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a profile lookup") {
		t.Errorf("want missing lookup error, got %v", err)
	}
}

func TestOpenFromSpec_ControllerLookupError(t *testing.T) {
	lookup := func(string) (string, string, error) {
		return "", "", fmt.Errorf("profile not found")
	}
	_, err := storeurl.OpenStateStoreFromSpec(context.Background(),
		backends.Spec{Type: backends.TypeController, Controller: "missing"}, lookup)
	if err == nil || !strings.Contains(err.Error(), "profile not found") {
		t.Errorf("want propagated profile error, got %v", err)
	}
}

var _ = errors.New
