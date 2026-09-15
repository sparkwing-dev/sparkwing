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
	if testing.Short() {
		t.Skip("slow: 2.0s of real work; the fast class runs under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixture")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	binary := filepath.Join(t.TempDir(), "describe")
	writeExec(t, binary, "#!/bin/sh\nexec sleep 5\n")
	started := time.Now()
	_, err := refreshDescribeFromBinary(context.Background(), t.TempDir(), binary, "fixture")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error=%v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("describe waited %v", elapsed)
	}
}
