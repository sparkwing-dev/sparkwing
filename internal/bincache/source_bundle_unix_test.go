//go:build !windows

package bincache

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSourceBundlePreservesCancellationDuringGit(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nkill -USR1 $PPID\nwhile :; do :; done\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-done:
		}
	}()
	data, transport := sourceBundleFixture([]byte("bundle"))
	dir := t.TempDir()
	_, err := FetchSourceBundle(ctx, data, transport.want, strings.Repeat("a", 40), "", dir)
	assertEmptyDirectory(t, dir)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation is not preserved: %v", err)
	}
}
