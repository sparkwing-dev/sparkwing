//go:build !windows

package bincache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedCompileWriter struct{}

func (failedCompileWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCompilePipelineUsesCapturedToolAndDiagnostics(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "go")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'captured Go output\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	options := CompileOptions{Environment: []string{"PATH=" + bin, "GOWORK=off"}}
	var out bytes.Buffer
	options.Diagnostics = &out
	if err := CompilePipeline(t.Context(), dir, filepath.Join(t.TempDir(), "pipeline"), options); err != nil || out.String() != "captured Go output\n" {
		t.Fatalf("captured compiler: error=%v output=%q", err, &out)
	}
	options.Diagnostics = failedCompileWriter{}
	if err := CompilePipeline(t.Context(), dir, filepath.Join(t.TempDir(), "pipeline"), options); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("lost compiler diagnostic error: %v", err)
	}
}

func TestGoCommandsRequireExplicitInputs(t *testing.T) {
	for _, options := range []CompileOptions{{}, {Environment: os.Environ()}, {Diagnostics: io.Discard}} {
		if err := CompilePipeline(t.Context(), t.TempDir(), "unused", options); err == nil {
			t.Fatal("compiler inferred missing inputs")
		}
		if err := runGo(t.Context(), t.TempDir(), nil, options); err == nil {
			t.Fatal("Go command inferred missing inputs")
		}
		if _, _, err := MaterializePipeline(t.Context(), t.TempDir(), options); err == nil {
			t.Fatal("materialization inferred missing inputs")
		}
	}
	options := CompileOptions{Environment: []string{"PATH=" + t.TempDir()}, Diagnostics: io.Discard}
	if err := CompilePipeline(t.Context(), t.TempDir(), "unused", options); err == nil || !strings.Contains(err.Error(), "Go 1.26+") {
		t.Fatalf("missing captured compiler: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	options.Environment = os.Environ()
	if err := CompilePipeline(ctx, t.TempDir(), "unused", options); !errors.Is(err, context.Canceled) {
		t.Fatalf("compiler ignored cancellation: %v", err)
	}
}
