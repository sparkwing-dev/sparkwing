package localws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestVersionHandler_ReportsVersionAndSchema(t *testing.T) {
	srv := httptest.NewServer(versionHandler("v0.16.0"))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.Version != "v0.16.0" {
		t.Errorf("version = %q, want v0.16.0", info.Version)
	}
	if info.Schema != store.ExpectedSchemaVersion() {
		t.Errorf("schema = %d, want %d", info.Schema, store.ExpectedSchemaVersion())
	}
	if info.PID == 0 {
		t.Error("pid = 0, want the running process id")
	}
}

// TestSchemaGuard_ExitsWhenDatabaseAdvances verifies the guard cancels
// the server context once the shared store is migrated past the schema
// this binary understands, so a resident dashboard shuts down cleanly
// instead of serving 500s forever.
func TestSchemaGuard_ExitsWhenDatabaseAdvances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := newSchemaGuard(st, cancel)

	guard.check(ctx)
	select {
	case <-ctx.Done():
		t.Fatal("guard cancelled before any skew")
	default:
	}

	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		future, 1); err != nil {
		t.Fatalf("seed future version: %v", err)
	}

	guard.check(ctx)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("guard did not cancel after the database advanced")
	}
}

// TestSchemaGuard_MiddlewareChecksOn5xx confirms a server error triggers
// a schema check at request time: with the database already advanced, a
// single failing request drives the clean shutdown.
func TestSchemaGuard_MiddlewareChecksOn5xx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard_mw.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	future := store.ExpectedSchemaVersion() + 1
	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_schema_version (version, applied_at) VALUES (?, ?)`,
		future, 1); err != nil {
		t.Fatalf("seed future version: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := newSchemaGuard(st, cancel)

	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	h := guard.middleware(failing)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("middleware did not trigger shutdown on a 5xx with an advanced schema")
	}
}

// TestSchemaGuard_MiddlewarePassesThroughOn2xx confirms a healthy
// response leaves the server running.
func TestSchemaGuard_MiddlewarePassesThroughOn2xx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard_ok.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := newSchemaGuard(st, cancel)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fine"))
	})
	h := guard.middleware(ok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil))

	time.Sleep(10 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatal("healthy request wrongly triggered shutdown")
	default:
	}
}
