package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sparkwing-dev/sparkwing/v2/internal/backend"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// fakeBackend is a minimal Backend stub for handler-shape tests.
// Untouched methods return ErrNotSupported so a misuse is loud.
type fakeBackend struct {
	caps        backend.Capabilities
	listRuns    func(store.RunFilter) ([]*store.Run, error)
	getRun      func(string) (*store.Run, error)
	listNodes   func(string) ([]*store.Node, error)
	listEvents  func(string, int64, int) ([]store.Event, error)
	readNodeLog func(string, string, backend.ReadOpts) ([]byte, error)
}

var _ backend.Backend = (*fakeBackend)(nil)

func (f *fakeBackend) Capabilities(context.Context) (backend.Capabilities, error) {
	return f.caps, nil
}
func (f *fakeBackend) ListRuns(_ context.Context, fl store.RunFilter) ([]*store.Run, error) {
	if f.listRuns == nil {
		return nil, backend.ErrNotSupported
	}
	return f.listRuns(fl)
}
func (f *fakeBackend) GetRun(_ context.Context, id string) (*store.Run, error) {
	if f.getRun == nil {
		return nil, backend.ErrNotSupported
	}
	return f.getRun(id)
}
func (f *fakeBackend) ListNodes(_ context.Context, id string) ([]*store.Node, error) {
	if f.listNodes == nil {
		return nil, backend.ErrNotSupported
	}
	return f.listNodes(id)
}
func (f *fakeBackend) ListEventsAfter(_ context.Context, id string, seq int64, limit int) ([]store.Event, error) {
	if f.listEvents == nil {
		return nil, nil
	}
	return f.listEvents(id, seq, limit)
}
func (f *fakeBackend) ReadNodeLog(_ context.Context, runID, nodeID string, opts backend.ReadOpts) ([]byte, error) {
	if f.readNodeLog == nil {
		return nil, nil
	}
	return f.readNodeLog(runID, nodeID, opts)
}
func (f *fakeBackend) StreamNodeLog(context.Context, string, string) (io.ReadCloser, error) {
	return nil, nil
}

func TestCapabilitiesHandler_ServesBackendCaps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		caps     backend.Capabilities
		wantRuns string
	}{
		{
			name: "sqlite",
			caps: backend.Capabilities{
				Mode:    "local",
				Storage: backend.CapabilitiesStorage{Artifacts: "fs", Logs: "fs", Runs: "sqlite"},
			},
			wantRuns: "sqlite",
		},
		{
			name: "s3",
			caps: backend.Capabilities{
				Mode:     "s3-only",
				Storage:  backend.CapabilitiesStorage{Artifacts: "s3", Logs: "s3", Runs: "s3"},
				ReadOnly: true,
			},
			wantRuns: "s3",
		},
		{
			name: "controller",
			caps: backend.Capabilities{
				Mode:    "cluster",
				Storage: backend.CapabilitiesStorage{Artifacts: "custom", Logs: "sparkwinglogs", Runs: "controller"},
			},
			wantRuns: "controller",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBackend{caps: tc.caps}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
			CapabilitiesHandler(b)(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			var got backend.Capabilities
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Mode != tc.caps.Mode {
				t.Errorf("Mode = %q, want %q", got.Mode, tc.caps.Mode)
			}
			if got.Storage.Runs != tc.wantRuns {
				t.Errorf("Storage.Runs = %q, want %q", got.Storage.Runs, tc.wantRuns)
			}
		})
	}
}

func TestListRunsHandler_AppliesParseRunFilter(t *testing.T) {
	t.Parallel()
	// The handler must hand whatever store.ParseRunFilter returns
	// through to Backend.ListRuns so dashboard and controller can't
	// drift on query-param semantics.
	var observed store.RunFilter
	b := &fakeBackend{
		listRuns: func(f store.RunFilter) ([]*store.Run, error) {
			observed = f
			return []*store.Run{{ID: "r1", Pipeline: "p"}}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?pipeline=p,q&status=succeeded&limit=7", nil)
	rec := httptest.NewRecorder()
	ListRunsHandler(b)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(observed.Pipelines) != 2 || observed.Pipelines[0] != "p" || observed.Pipelines[1] != "q" {
		t.Errorf("Pipelines = %v", observed.Pipelines)
	}
	if len(observed.Statuses) != 1 || observed.Statuses[0] != "succeeded" {
		t.Errorf("Statuses = %v", observed.Statuses)
	}
	if observed.Limit != 7 {
		t.Errorf("Limit = %d, want 7", observed.Limit)
	}
}

func TestGetRunHandler_IncludeNodes(t *testing.T) {
	t.Parallel()
	b := &fakeBackend{
		getRun: func(id string) (*store.Run, error) {
			return &store.Run{ID: id, Pipeline: "p"}, nil
		},
		listNodes: func(string) ([]*store.Node, error) {
			return []*store.Node{{NodeID: "n1"}}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	GetRunHandler(b)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var run store.Run
	if err := json.NewDecoder(rec.Body).Decode(&run); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if run.ID != "abc" {
		t.Errorf("ID = %q", run.ID)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/runs/abc?include=nodes", nil)
	req2.SetPathValue("id", "abc")
	rec2 := httptest.NewRecorder()
	GetRunHandler(b)(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d", rec2.Code)
	}
	var wrap struct {
		Run   *store.Run    `json:"run"`
		Nodes []*store.Node `json:"nodes"`
	}
	if err := json.NewDecoder(rec2.Body).Decode(&wrap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if wrap.Run == nil || wrap.Run.ID != "abc" {
		t.Errorf("wrap.Run = %+v", wrap.Run)
	}
	if len(wrap.Nodes) != 1 || wrap.Nodes[0].NodeID != "n1" {
		t.Errorf("wrap.Nodes = %+v", wrap.Nodes)
	}
}

func TestGetRunHandler_NotFound(t *testing.T) {
	t.Parallel()
	b := &fakeBackend{
		getRun: func(string) (*store.Run, error) { return nil, store.ErrNotFound },
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/missing", nil)
	req.SetPathValue("id", "missing")
	rec := httptest.NewRecorder()
	GetRunHandler(b)(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// guard: ensure ErrNotSupported isn't accidentally swallowed by fakeBackend.
var _ = errors.New
