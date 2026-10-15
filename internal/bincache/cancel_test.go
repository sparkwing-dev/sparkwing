package bincache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCompilePipelineCancelStopsTheBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CompilePipeline(ctx, t.TempDir(), filepath.Join(t.TempDir(), "bin", "pipelines"), CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CompilePipeline error = %v, want the cancellation", err)
	}
}
