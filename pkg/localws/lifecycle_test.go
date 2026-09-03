package localws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

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

func TestSchemaGuard_ExitsOnUnknownRequirement(t *testing.T) {
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

	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_requirements (name, added_at, added_by_version)
		 VALUES ('webhook-replay-keys', 1, 'v0.41.0')`); err != nil {
		t.Fatalf("seed future requirement: %v", err)
	}

	guard.check(ctx)
	select {
	case <-ctx.Done():
	default:
		t.Fatal("guard did not cancel after the database gained an unknown requirement")
	}
}

func TestSchemaGuard_MiddlewareChecksOn5xx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard_mw.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	if _, err := st.DB().Exec(
		`INSERT INTO sparkwing_requirements (name, added_at, added_by_version)
		 VALUES ('webhook-replay-keys', 1, 'v0.41.0')`); err != nil {
		t.Fatalf("seed future requirement: %v", err)
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
		t.Fatal("middleware did not trigger shutdown on a 5xx with an unknown requirement")
	}
}

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

	select {
	case <-ctx.Done():
		t.Fatal("healthy request wrongly triggered shutdown")
	default:
	}
}
