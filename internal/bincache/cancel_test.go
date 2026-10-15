package bincache

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestCompilePipelineCancelStopsTheBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CompilePipeline(ctx, t.TempDir(), filepath.Join(t.TempDir(), "bin", "pipelines"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CompilePipeline error = %v, want the cancellation", err)
	}
}
