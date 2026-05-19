package controller_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// fakeArtifactStore is a tiny in-memory ArtifactStore for tests.
// Avoids spinning up the fs/s3 backends here -- this file's job is
// to verify the handler + route-gating wiring, not the storage impl.
type fakeArtifactStore struct {
	objects map[string][]byte
}

func (f *fakeArtifactStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeArtifactStore) Put(context.Context, string, io.Reader) error { return nil }
func (f *fakeArtifactStore) Has(context.Context, string) (bool, error)    { return false, nil }
func (f *fakeArtifactStore) Delete(context.Context, string) error         { return nil }
func (f *fakeArtifactStore) List(context.Context, string) ([]string, error) {
	return nil, nil
}

// newServerWithArtifacts wires WithArtifactStore (or doesn't) before
// binding the handler, mirroring how pkg/localws calls New(...).
// WithArtifactStore(...) at startup.
func newServerWithArtifacts(t *testing.T, art storage.ArtifactStore) string {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctrl := controller.New(s, nil).WithArtifactStore(art)
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// Without WithArtifactStore the route is unregistered entirely, so the
// request 404s through the outer mux. This is the laptop-vs-cluster
// hinge: cluster mode never carries an ArtifactStore so cluster
// callers must not see this endpoint at all.
func TestArtifactsEndpoint_RouteAbsentWhenUnconfigured(t *testing.T) {
	t.Parallel()
	base := newServerWithArtifacts(t, nil)

	resp, err := http.Get(base + "/api/v1/artifacts/abcd1234")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// With WithArtifactStore the route serves bytes for known keys and
// 404s for missing keys.
func TestArtifactsEndpoint_RoundTrip(t *testing.T) {
	t.Parallel()
	art := &fakeArtifactStore{
		objects: map[string][]byte{"good-key": []byte("payload")},
	}
	base := newServerWithArtifacts(t, art)

	resp, err := http.Get(base + "/api/v1/artifacts/good-key")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "payload" {
		t.Errorf("body = %q", body)
	}

	resp2, err := http.Get(base + "/api/v1/artifacts/missing")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("missing status = %d", resp2.StatusCode)
	}
}

// Pool routes are off when AttachPool is not called (laptop mode).
// Cluster-mode tests for the configured pool live alongside pool.go.
func TestPoolRoutes_AbsentWhenUnattached(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv := httptest.NewServer(controller.New(s, nil).Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/pool")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/v1/pool: status=%d want 404", resp.StatusCode)
	}
}

// WithReconcileHook installs a closure that fires on every list-runs
// and get-run. A nil hook leaves reads completely untouched.
func TestReconcileHook_RunsBeforeReads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var hookCalls atomic.Int32
	ctrl := controller.New(s, nil).
		WithReconcileHook(func(_ context.Context) error {
			hookCalls.Add(1)
			return nil
		})
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)

	// list-runs read
	resp, err := http.Get(srv.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := hookCalls.Load(); got != 1 {
		t.Errorf("after list: hook calls=%d want 1", got)
	}

	// get-run read (404 path still triggers the hook -- it's
	// pre-read, not post-validation)
	resp2, err := http.Get(srv.URL + "/api/v1/runs/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got := hookCalls.Load(); got != 2 {
		t.Errorf("after get: hook calls=%d want 2", got)
	}
}

// A nil reconcile hook is the default and must not introduce any
// wrapper -- write-side handlers should be untouched and reads must
// still work.
func TestReconcileHook_NoHookIsPassThrough(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	srv := httptest.NewServer(controller.New(s, nil).Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
