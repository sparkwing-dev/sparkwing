package backend_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func TestFromSpecs_SQLite(t *testing.T) {
	dir := t.TempDir()
	paths := newTempPaths(t, dir)
	stateSpec := &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(dir, "state.db")}

	b, closer, err := backend.FromSpecs(context.Background(), stateSpec, nil, nil, paths, nil)
	if err != nil {
		t.Fatalf("FromSpecs: %v", err)
	}
	defer func() { _ = closer.Close() }()

	caps, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.Storage.Runs != "sqlite" {
		t.Errorf("Storage.Runs = %q, want sqlite", caps.Storage.Runs)
	}
	if caps.Mode != "local" {
		t.Errorf("Mode = %q, want local", caps.Mode)
	}
}

func TestFromSpecs_RefusesRemovedStateTypes(t *testing.T) {
	paths := newTempPaths(t, t.TempDir())
	for _, spec := range []backends.Spec{
		{Type: backends.TypeS3, Bucket: "b"},
		{Type: "postgres", URL: "postgres://db/sw"},
	} {
		if _, _, err := backend.FromSpecs(context.Background(), &spec, nil, nil, paths, nil); err == nil {
			t.Errorf("FromSpecs(%s) opened a removed state type", spec.Type)
		}
	}
}

func TestFromSpecs_Controller(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	lookup := func(name string) (string, string, error) {
		if name != "prod" {
			t.Fatalf("unexpected profile lookup %q", name)
		}
		return srv.URL, "tok", nil
	}
	paths := newTempPaths(t, t.TempDir())
	stateSpec := &backends.Spec{Type: backends.TypeController, Controller: "prod"}

	b, closer, err := backend.FromSpecs(context.Background(), stateSpec, nil, nil, paths, storeurl.ProfileLookup(lookup))
	if err != nil {
		t.Fatalf("FromSpecs(controller): %v", err)
	}
	defer func() { _ = closer.Close() }()

	caps, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.Storage.Runs != "controller" {
		t.Errorf("Storage.Runs = %q, want controller", caps.Storage.Runs)
	}
	if caps.Mode != "cluster" {
		t.Errorf("Mode = %q, want cluster", caps.Mode)
	}
}

func TestFromSpecs_NilState(t *testing.T) {
	paths := newTempPaths(t, t.TempDir())
	_, _, err := backend.FromSpecs(context.Background(), nil, nil, nil, paths, nil)
	if err == nil {
		t.Fatal("expected error when state spec is nil")
	}
}

func TestFromSpecs_CapabilitiesTagsFlow(t *testing.T) {
	dir := t.TempDir()
	paths := newTempPaths(t, dir)
	stateSpec := &backends.Spec{Type: backends.TypeSQLite, Path: filepath.Join(dir, "state.db")}
	logsSpec := &backends.Spec{Type: backends.TypeFilesystem, Path: filepath.Join(dir, "logs")}

	b, closer, err := backend.FromSpecs(context.Background(), stateSpec, logsSpec, nil, paths, nil)
	if err != nil {
		t.Fatalf("FromSpecs: %v", err)
	}
	defer func() { _ = closer.Close() }()

	caps, _ := b.Capabilities(context.Background())
	if caps.Storage.Runs != "sqlite" {
		t.Errorf("Runs = %q, want sqlite", caps.Storage.Runs)
	}
	if caps.Storage.Logs != "filesystem" {
		t.Errorf("Logs = %q, want filesystem", caps.Storage.Logs)
	}
}

func newTempPaths(t *testing.T, root string) paths.Paths {
	t.Helper()
	return paths.Paths{Root: root}
}
