package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalRunnerExitsOnceItsControllerStaysGone(t *testing.T) {
	ticks, answers := make(chan time.Time), make(chan bool)
	done := make(chan error, 1)
	go func() {
		done <- awaitControllerGone(t.Context(), t.TempDir(), func(context.Context) bool { return <-answers }, ticks, 10*time.Minute)
	}()
	start := time.Unix(0, 0)
	probe := func(at time.Duration, reachable bool) {
		t.Helper()
		select {
		case ticks <- start.Add(at):
		case err := <-done:
			t.Fatalf("runner exited before the probe at %s: %v", at, err)
		}
		answers <- reachable
	}
	probe(0, false)
	probe(9*time.Minute, false)
	// safety: a replacement controller that answers restarts the bound.
	probe(9*time.Minute+30*time.Second, true)
	probe(10*time.Minute, false)
	probe(19*time.Minute, false)
	probe(20*time.Minute, false)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "no controller has served") {
		t.Fatalf("runner outlived its controller: %v", err)
	}
}

func TestLocalRunnerExitsAtOnceWhenItsHomeIsGone(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan time.Time, 1)
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Unix(0, 0)
	err := awaitControllerGone(t.Context(), home, func(context.Context) bool { return true }, ticks, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("runner outlived its home: %v", err)
	}
}

func TestLocalRunnerControllerWatchEndsWithItsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := awaitControllerGone(ctx, t.TempDir(), func(context.Context) bool { return false }, make(chan time.Time), time.Minute); err != nil {
		t.Fatalf("a finished runner reported its controller gone: %v", err)
	}
}
