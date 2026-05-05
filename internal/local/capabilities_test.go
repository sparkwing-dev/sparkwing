package local_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	controller "github.com/sparkwing-dev/sparkwing/internal/local"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// fakeArtifactStore is a tiny in-memory ArtifactStore for tests.
// Avoids spinning up the fs/s3 backends here -- this file's job is
// to verify the handler wiring, not the storage impl.
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

// newServerWithStorage runs SetArtifactStore before binding the
// handler, mirroring how pkg/localws wires things at startup.
func newServerWithStorage(t *testing.T, art storage.ArtifactStore) string {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctrl := controller.New(s, nil)
	if art != nil {
		ctrl.SetArtifactStore(art)
	}
	srv := httptest.NewServer(ctrl.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestArtifactsEndpoint_NoStore(t *testing.T) {
	// With no ArtifactStore configured, the route returns 404 even
	// for hashed keys that "look real". Frontend can probe with one
	// GET to discover whether the feature is available.
	t.Parallel()
	base := newServerWithStorage(t, nil)

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
	base := newServerWithStorage(t, art)

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
