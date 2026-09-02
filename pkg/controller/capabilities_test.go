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

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type fakeArtifactStore struct {
	objects map[string][]byte
	gets    []string
}

func (f *fakeArtifactStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.gets = append(f.gets, key)
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

func TestArtifactsEndpoint_RejectsTraversalKey(t *testing.T) {
	t.Parallel()
	const target = "/api/v1/artifacts/..%2f..%2fetc%2fpasswd"

	t.Run("server", func(t *testing.T) {
		t.Parallel()
		art := &fakeArtifactStore{}
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "state.db"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		h := controller.New(st, nil).WithArtifactStore(art).Handler()

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if len(art.gets) != 0 {
			t.Errorf("store reached with keys %v", art.gets)
		}
	})

	t.Run("loopback", func(t *testing.T) {
		t.Parallel()
		art := &fakeArtifactStore{}
		h := controller.NewLoopback(nil, "run-1", "", nil).WithArtifactStore(art).Handler()

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
		if len(art.gets) != 0 {
			t.Errorf("store reached with keys %v", art.gets)
		}
	})
}

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

	resp, err := http.Get(srv.URL + "/api/v1/runs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := hookCalls.Load(); got != 1 {
		t.Errorf("after list: hook calls=%d want 1", got)
	}

	resp2, err := http.Get(srv.URL + "/api/v1/runs/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got := hookCalls.Load(); got != 2 {
		t.Errorf("after get: hook calls=%d want 2", got)
	}
}

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
