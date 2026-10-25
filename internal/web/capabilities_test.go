package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type fakeBackend struct {
	caps        backend.Capabilities
	listRuns    func(store.RunFilter) ([]*store.Run, error)
	getRun      func(string) (*store.Run, error)
	listNodes   func(string) ([]*store.Node, error)
	listEvents  func(string, int64, int) ([]store.Event, error)
	readNodeLog func(string, string, backend.ReadOpts) ([]byte, error)
	streamLog   func(string, string) (io.ReadCloser, error)
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

func (f *fakeBackend) StreamNodeLog(_ context.Context, runID, nodeID string) (io.ReadCloser, error) {
	if f.streamLog != nil {
		return f.streamLog(runID, nodeID)
	}
	return nil, nil
}

func TestEventsStreamUsesCanonicalPublicProjection(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	b := &fakeBackend{
		getRun: func(string) (*store.Run, error) {
			return &store.Run{RunSummary: runstate.RunSummary{ID: "run-private-event", Status: "success"}}, nil
		},
		listEvents: func(_ string, after int64, _ int) ([]store.Event, error) {
			if after > 0 {
				return nil, nil
			}
			return []store.Event{{
				RunID: "run-private-event", Seq: 1, Kind: "execution_attempt_started",
				Payload: json.RawMessage(`{"claim_generation":"private-generation-value","attempt":2,"executor_name":"desktop"}`),
			}}, nil
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-private-event/events", nil)
	rec := httptest.NewRecorder()
	serveEventsStream(b, rec, req, "run-private-event")

	body := rec.Body.String()
	for _, private := range []string{`"claim_generation"`, "private-generation-value"} {
		if strings.Contains(body, private) {
			t.Errorf("local event stream exposed %q: %s", private, body)
		}
	}
	for _, public := range []string{`"attempt":2`, `"executor_name":"desktop"`} {
		if !strings.Contains(body, public) {
			t.Errorf("local event stream omitted %q: %s", public, body)
		}
	}
}

var _ = errors.New
