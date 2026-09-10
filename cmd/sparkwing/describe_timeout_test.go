package main

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDescribeCacheBoundsUnresponsiveBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	binary := filepath.Join(t.TempDir(), "describe")
	writeExec(t, binary, "#!/bin/sh\nexec sleep 5\n")
	started := time.Now()
	_, err := refreshDescribeFromBinary(t.TempDir(), binary, "fixture")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error=%v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("describe waited %v", elapsed)
	}
}
