package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type memBucket struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemBucket() *memBucket { return &memBucket{data: map[string][]byte{}} }

func (m *memBucket) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memBucket) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = body
	return nil
}

func (m *memBucket) Has(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memBucket) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memBucket) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func TestStartRunLoopback_SQLiteStillGetsTheRealController(t *testing.T) {
	paths := newInternalPaths(t)
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	backends := LocalBackends(paths, st, nil)
	loopback, err := startRunLoopback(t.Context(), &Options{State: st, RunID: "run-sqlite"}, backends, quietTestLogger())
	if err != nil {
		t.Fatalf("startRunLoopback: %v", err)
	}
	t.Cleanup(loopback.Close)

	req, _ := http.NewRequest(http.MethodGet, loopback.url+"/api/v1/trends", nil)
	req.Header.Set("Authorization", "Bearer "+loopback.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("trends probe: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Error("a SQLite-backed run got the node-facing shim, not the real controller")
	}
}

func newInternalPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	p := PathsAt(root)
	if err := p.EnsureRoot(); err != nil {
		t.Fatalf("ensure root: %v", err)
	}
	return p
}

func quietTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSetupLocalExecution_CanceledContextDoesNotMintToken(t *testing.T) {
	paths := newInternalPaths(t)
	st, err := teststore.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	execution, err := setupLocalExecution(ctx, paths, &Options{State: st, RunID: "canceled"}, LocalBackends(paths, st, nil), t.TempDir(), quietTestLogger())
	if execution != nil {
		execution.cleanup()
		t.Fatal("canceled setup returned an execution")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("setup error = %v; want context cancellation", err)
	}
	tokens, err := st.ListTokens("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 0 {
		t.Fatalf("canceled setup persisted %d tokens", len(tokens))
	}
}
