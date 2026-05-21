package backend_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

func TestParseInlineSpec(t *testing.T) {
	cases := []struct {
		in   string
		want backends.Spec
	}{
		{"sqlite:///tmp/state.db", backends.Spec{Type: backends.TypeSQLite, Path: "/tmp/state.db"}},
		{"postgres://u:p@h:5432/d?sslmode=disable", backends.Spec{Type: backends.TypePostgres, URL: "postgres://u:p@h:5432/d?sslmode=disable"}},
		{"postgresql://u:p@h:5432/d", backends.Spec{Type: backends.TypePostgres, URL: "postgresql://u:p@h:5432/d"}},
		{"s3://my-bucket/runs", backends.Spec{Type: backends.TypeS3, Bucket: "my-bucket", Prefix: "runs"}},
		{"s3://only-bucket", backends.Spec{Type: backends.TypeS3, Bucket: "only-bucket"}},
		{"gcs://b/p", backends.Spec{Type: backends.TypeGCS, Bucket: "b", Prefix: "p"}},
		{"azure-blob://b/p", backends.Spec{Type: backends.TypeAzureBlob, Bucket: "b", Prefix: "p"}},
		{"controller://prod", backends.Spec{Type: backends.TypeController, Controller: "prod"}},
		{"fs:///var/log/sparkwing", backends.Spec{Type: backends.TypeFilesystem, Path: "/var/log/sparkwing"}},
		{"stdout:", backends.Spec{Type: backends.TypeStdout}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := backend.ParseInlineSpec(tc.in)
			if err != nil {
				t.Fatalf("ParseInlineSpec(%q): %v", tc.in, err)
			}
			if got == nil || !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("ParseInlineSpec(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseInlineSpec_Empty(t *testing.T) {
	got, err := backend.ParseInlineSpec("  ")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("empty input should return nil spec, got %+v", got)
	}
}

func TestParseInlineSpec_Unknown(t *testing.T) {
	_, err := backend.ParseInlineSpec("redis://localhost:6379")
	if err == nil {
		t.Fatal("expected error for unknown scheme")
	}
	if !strings.Contains(err.Error(), "unknown spec scheme") {
		t.Errorf("want 'unknown spec scheme', got: %v", err)
	}
}

func TestParseInlineSpec_ControllerMissingProfile(t *testing.T) {
	_, err := backend.ParseInlineSpec("controller://")
	if err == nil {
		t.Fatal("expected error when profile is missing")
	}
}

// TestFromSpecs_SQLite is the always-on smoke test: a fresh SQLite
// path resolves into a StoreBackend with the right capability tags.
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

// TestFromSpecs_Postgres exercises the pg path. Env-gated so a
// developer without Postgres still gets a green run.
func TestFromSpecs_Postgres(t *testing.T) {
	dsn := os.Getenv("SPARKWING_TEST_PG_URL")
	if dsn == "" {
		t.Skip("SPARKWING_TEST_PG_URL not set; skipping pg FromSpecs test")
	}
	paths := newTempPaths(t, t.TempDir())
	stateSpec := &backends.Spec{Type: backends.TypePostgres, URL: dsn}

	b, closer, err := backend.FromSpecs(context.Background(), stateSpec, nil, nil, paths, nil)
	if err != nil {
		t.Fatalf("FromSpecs(postgres): %v", err)
	}
	defer func() { _ = closer.Close() }()

	caps, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.Storage.Runs != "postgres" {
		t.Errorf("Storage.Runs = %q, want postgres", caps.Storage.Runs)
	}
	if caps.Mode != "shared-db" {
		t.Errorf("Mode = %q, want shared-db", caps.Mode)
	}
}

// TestFromSpecs_Controller verifies the hosted-controller branch.
// The httptest server only needs to respond to one capabilities-probe
// path; FromSpecs itself doesn't make any HTTP calls, only the
// resulting Backend would when the dashboard hits it.
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

// TestFromSpecs_NilState rejects a missing state spec.
func TestFromSpecs_NilState(t *testing.T) {
	paths := newTempPaths(t, t.TempDir())
	_, _, err := backend.FromSpecs(context.Background(), nil, nil, nil, paths, nil)
	if err == nil {
		t.Fatal("expected error when state spec is nil")
	}
}

// TestFromSpecs_CapabilitiesTagsFlow confirms the resolved spec types
// flow through to Storage.{Runs,Logs,Artifacts} verbatim. The dashboard
// SPA uses these tags to adapt UI; they need to be the spec's Type, not
// the impl's hardcoded default.
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

func newTempPaths(t *testing.T, root string) orchestrator.Paths {
	t.Helper()
	return orchestrator.Paths{Root: root}
}
