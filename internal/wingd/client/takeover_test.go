package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func stuckOlderDaemon(t *testing.T, home, version string) *atomic.Int64 {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	handshakes := &atomic.Int64{}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				r := newFrameReader(nc)
				if _, err := r.read(); err != nil {
					return
				}
				handshakes.Add(1)
				line, _ := wingwire.Encode(&wingwire.HelloAck{
					ProtocolMajor:       wingd.ProtocolMajor,
					NativeProtocolMajor: wingd.ProtocolMajor,
					BinaryVersion:       version,
				})
				if _, err := nc.Write(line); err != nil {
					return
				}
				_, _ = r.read()
			}()
		}
	}()
	return handshakes
}

func TestTakeoverStopsAfterItsAttemptBudget(t *testing.T) {
	home := shortHome(t)
	handshakes := stuckOlderDaemon(t, home, "v0.1.0")
	spawns := &atomic.Int64{}

	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v9.9.9",
		Spawn:       func(string, string) error { spawns.Add(1); return nil },
		DialTimeout: 200 * time.Millisecond,
		Backoff:     5 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("a daemon that never yields returned a usable client")
	}
	if !strings.Contains(err.Error(), "v0.1.0") || !strings.Contains(err.Error(), "v9.9.9") {
		t.Fatalf("takeover failure = %v, want it to name both versions", err)
	}
	if got := spawns.Load(); got != int64(maxTakeoverAttempts) {
		t.Fatalf("successor spawns = %d, want %d", got, maxTakeoverAttempts)
	}
	if got := handshakes.Load(); got > int64(maxTakeoverAttempts)+2 {
		t.Fatalf("handshakes = %d; the takeover loop is unbounded", got)
	}
}

func TestTakeoverBudgetRestartsForADifferentDaemon(t *testing.T) {
	home := shortHome(t)
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	const predecessors = 5
	handshakes := &atomic.Int64{}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			seen := handshakes.Add(1)
			go func() {
				defer nc.Close()
				r := newFrameReader(nc)
				if _, err := r.read(); err != nil {
					return
				}
				version := "v9.9.9"
				if seen <= predecessors {
					version = fmt.Sprintf("v0.%d.0", seen)
				}
				line, _ := wingwire.Encode(&wingwire.HelloAck{
					ProtocolMajor:       wingd.ProtocolMajor,
					NativeProtocolMajor: wingd.ProtocolMajor,
					BinaryVersion:       version,
				})
				if _, err := nc.Write(line); err != nil {
					return
				}
				_, _ = r.read()
			}()
		}
	}()

	spawns := &atomic.Int64{}
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v9.9.9",
		Spawn:       func(string, string) error { spawns.Add(1); return nil },
		DialTimeout: 200 * time.Millisecond,
		Backoff:     5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("five different predecessors exhausted a budget meant for one: %v", err)
	}
	defer cl.Close()

	if got := spawns.Load(); got != predecessors {
		t.Fatalf("successor spawns = %d, want one per predecessor (%d)", got, predecessors)
	}
}

func TestAlternatingPredecessorsStillExhaustTheBudget(t *testing.T) {
	home := shortHome(t)
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	handshakes := &atomic.Int64{}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			seen := handshakes.Add(1)
			go func() {
				defer nc.Close()
				r := newFrameReader(nc)
				if _, err := r.read(); err != nil {
					return
				}
				version := "v0.23.0"
				if seen%2 == 0 {
					version = "v0.24.0"
				}
				line, _ := wingwire.Encode(&wingwire.HelloAck{
					ProtocolMajor:       wingd.ProtocolMajor,
					NativeProtocolMajor: wingd.ProtocolMajor,
					BinaryVersion:       version,
				})
				if _, err := nc.Write(line); err != nil {
					return
				}
				_, _ = r.read()
			}()
		}
	}()

	spawns := &atomic.Int64{}
	done := make(chan error, 1)
	go func() {
		_, cerr := EnsureDaemon(context.Background(), Options{
			Home:        home,
			Version:     "v9.9.9",
			Spawn:       func(string, string) error { spawns.Add(1); return nil },
			DialTimeout: 200 * time.Millisecond,
			Backoff:     5 * time.Millisecond,
		})
		done <- cerr
	}()

	select {
	case err := <-done:
		if !errors.Is(err, ErrTakeoverExhausted) {
			t.Fatalf("alternating predecessors ended with %v, want the version conflict reported", err)
		}
		if got := spawns.Load(); got != int64(maxTotalTakeovers) {
			t.Fatalf("successor spawns = %d, want the total ceiling %d", got, maxTotalTakeovers)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("alternating predecessors never exhausted the takeover budget")
	}
}

func TestTakeoverBudgetRestartsPerVersionUpToATotalCeiling(t *testing.T) {
	budget := &takeoverBudget{}
	for i := 0; i < maxTakeoverAttempts; i++ {
		if !budget.spend("v0.1.0") {
			t.Fatalf("attempt %d against one version was refused early", i+1)
		}
	}
	if budget.spend("v0.1.0") {
		t.Fatal("the same version kept coming back and was taken over a fourth time")
	}
	if !budget.spend("v0.2.0") {
		t.Fatal("a different predecessor was refused; replacing it is progress")
	}
	for budget.total < maxTotalTakeovers {
		if !budget.spend(fmt.Sprintf("v0.%d.0", budget.total+3)) {
			t.Fatalf("budget refused at %d, before the total ceiling %d", budget.total, maxTotalTakeovers)
		}
	}
	if budget.spend("v0.99.0") {
		t.Fatalf("budget allowed a %dth takeover past the ceiling %d", budget.total+1, maxTotalTakeovers)
	}
}
