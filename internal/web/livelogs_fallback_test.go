package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

type liveBackend struct {
	fakeBackend
	stream func(string, string, int64) (io.ReadCloser, error)
}

var _ backend.LiveLogReader = (*liveBackend)(nil)

func (b *liveBackend) StreamNodeLiveLog(_ context.Context, runID, nodeID string, since int64) (io.ReadCloser, error) {
	if b.stream == nil {
		return nil, nil
	}
	return b.stream(runID, nodeID, since)
}

func (b *liveBackend) ReadNodeLiveLog(context.Context, string, string, int64) ([]byte, int64, bool, bool, error) {
	return nil, 0, false, false, nil
}

func TestServeLogStream_FallsBackToTheControllersLiveRing(t *testing.T) {
	t.Parallel()
	b := &liveBackend{stream: func(string, string, int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("data: {\"msg\":\"live\"}\n\n")), nil
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/build/stream?format=raw", nil)
	serveLogStream(b, rec, req, "run-1", "build")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the live ring", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"live"`) {
		t.Fatalf("body = %q, want the live line", rec.Body.String())
	}
}

func TestServeLogStream_StaysNotImplementedWithoutALiveRing(t *testing.T) {
	t.Parallel()
	b := &liveBackend{}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/build/stream", nil)
	serveLogStream(b, rec, req, "run-1", "build")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when neither surface streams", rec.Code)
	}
}
