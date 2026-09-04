package bincache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestTryBinaryCancelStopsTheDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- TryBinary(ctx, srv.URL, "", "deadbeef-cafebabe", filepath.Join(t.TempDir(), "bin"))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("TryBinary error = %v, want the cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TryBinary waited on the cache past its cancelled context")
	}
}

func TestCompilePipelineCancelStopsTheBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CompilePipeline(ctx, t.TempDir(), filepath.Join(t.TempDir(), "bin", "pipelines"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CompilePipeline error = %v, want the cancellation", err)
	}
}
