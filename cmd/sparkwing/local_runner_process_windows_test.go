//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestWindowsPinnedLocalRunnerExecutableStarts(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pinned, _, err := pinLocalRunnerExecutable(t.TempDir(), executable)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(pinned) != ".exe" {
		t.Fatalf("pinned runner lacks executable suffix: %s", pinned)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if body, err := procenv.ControlCommandContext(ctx, os.Environ(), pinned, "-test.run=^$").CombinedOutput(); err != nil {
		t.Fatalf("start pinned runner: %v: %s", err, body)
	}
}
