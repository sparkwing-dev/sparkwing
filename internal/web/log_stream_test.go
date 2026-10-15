package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

type streamBackend struct {
	fakeBackend
	stream func(context.Context, string, string) (io.ReadCloser, error)
}

func (b *streamBackend) StreamNodeLog(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	if b.stream == nil {
		return nil, nil
	}
	return b.stream(ctx, runID, nodeID)
}

func TestServeLogStreamUsesBoundReaderAndResumeCursor(t *testing.T) {
	b := &streamBackend{stream: func(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
		if runID != "run-1" || nodeID != "build" || storage.LogCursor(ctx) != "resume" {
			t.Fatalf("stream selector = %q %q %q", runID, nodeID, storage.LogCursor(ctx))
		}
		return io.NopCloser(strings.NewReader("data: bound\n\n")), nil
	}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/build/stream?format=raw&cursor=ignored", nil)
	req.Header.Set("Last-Event-ID", "resume")
	serveLogStream(b, rec, req, "run-1", "build")
	if rec.Code != http.StatusOK || rec.Body.String() != "data: bound\n\n" {
		t.Fatalf("stream = %d %q", rec.Code, rec.Body.String())
	}
}

func TestServeLogStreamRequiresBoundReader(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1/logs/build/stream", nil)
	serveLogStream(&streamBackend{}, rec, req, "run-1", "build")
	if rec.Code != http.StatusNotImplemented || rec.Body.Len() != 0 {
		t.Fatalf("unbound stream = %d %q", rec.Code, rec.Body.String())
	}
}
