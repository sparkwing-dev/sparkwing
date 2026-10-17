//go:build !windows

package source_test

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestSourceBundlePreservesCancellationDuringGit(t *testing.T) {
	spec, options, _ := bundleFixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nkill -USR1 $PPID\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-done:
		}
	}()
	_, err := source.PrepareCheckout(ctx, spec, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("lost Git cancellation", err)
	}
	entries, err := os.ReadDir(options.Workspace)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled Git left files", entries, err)
	}
}
