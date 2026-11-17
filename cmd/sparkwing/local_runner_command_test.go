package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type controllerProbe struct {
	t       *testing.T
	ticks   chan time.Time
	answers chan bool
	done    chan error
	start   time.Time
}

func watchController(t *testing.T, home string) *controllerProbe {
	t.Helper()
	p := &controllerProbe{t: t, ticks: make(chan time.Time), answers: make(chan bool), done: make(chan error, 1), start: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		p.done <- awaitControllerGone(ctx, home, func(context.Context) bool { return <-p.answers }, p.ticks, 10*time.Minute, time.Minute)
	}()
	return p
}

func (p *controllerProbe) probe(at time.Duration, reachable bool) {
	p.t.Helper()
	select {
	case p.ticks <- p.start.Add(at):
	case err := <-p.done:
		p.t.Fatalf("runner exited before the probe at %s: %v", at, err)
	}
	select {
	case p.answers <- reachable:
	case err := <-p.done:
		p.t.Fatalf("runner exited at the probe at %s without asking its controller: %v", at, err)
	}
}

func (p *controllerProbe) exited() error {
	p.t.Helper()
	return <-p.done
}

func TestLocalRunnerExitsOnceItsControllerStaysGone(t *testing.T) {
	p := watchController(t, t.TempDir())
	p.probe(0, false)
	p.probe(9*time.Minute, false)
	// safety: a replacement controller that answers restarts the bound.
	p.probe(9*time.Minute+30*time.Second, true)
	p.probe(10*time.Minute, false)
	p.probe(19*time.Minute, false)
	p.probe(20*time.Minute, false)
	if err := p.exited(); err == nil || !strings.Contains(err.Error(), "no controller has accepted") {
		t.Fatalf("runner outlived its controller: %v", err)
	}
}

func TestLocalRunnerKeepsServingAReachableControllerWhileItsHomeIsMissing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	p := watchController(t, home)
	for at := time.Duration(0); at <= 30*time.Minute; at += 30 * time.Second {
		p.probe(at, true)
	}
	// safety: the next tick lands only if the watch is still waiting, so the runner kept serving.
	p.probe(31*time.Minute, true)
}

func TestLocalRunnerRidesOutABriefHomeAndControllerOutage(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "state")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	p := watchController(t, home)
	if err := os.Rename(home, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	p.probe(0, false)
	p.probe(30*time.Second, false)
	if err := os.Rename(filepath.Join(parent, "moved"), home); err != nil {
		t.Fatal(err)
	}
	p.probe(time.Minute, false)
	p.probe(90*time.Second, true)
	if err := os.Rename(home, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	p.probe(2*time.Minute, false)
	p.probe(2*time.Minute+30*time.Second, false)
	p.probe(3*time.Minute, true)
}

func TestLocalRunnerExitsWhenItsHomeAndControllerStayGone(t *testing.T) {
	p := watchController(t, filepath.Join(t.TempDir(), "state"))
	p.probe(0, false)
	p.probe(30*time.Second, false)
	p.probe(time.Minute, false)
	if err := p.exited(); err == nil || !strings.Contains(err.Error(), "has been gone") {
		t.Fatalf("runner outlived its deleted home and controller: %v", err)
	}
}

func TestLocalRunnerControllerWatchEndsWithItsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := awaitControllerGone(ctx, t.TempDir(), func(context.Context) bool { return false }, make(chan time.Time), time.Minute, time.Minute); err != nil {
		t.Fatalf("a finished runner reported its controller gone: %v", err)
	}
}
