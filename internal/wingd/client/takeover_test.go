package client

import (
	"context"
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

// stuckOlderDaemon answers every handshake as the same superseded
// version, whatever drain requests it is sent. It is what a spawn that
// keeps bringing up the old binary looks like from the client side.
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

// TestTakeoverStopsAfterItsAttemptBudget bounds the drain-respawn churn.
// A takeover that worked is followed by a connection to the successor, so
// a daemon that keeps coming back as the version it replaced is a skew an
// operator has to resolve -- not something to retry until the process
// ends.
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

// TestTakeoverBudgetRestartsForADifferentDaemon keeps the cap pointed at
// the loop it exists for. Several different old daemons winning the
// socket race in turn is progress -- each one is replaced -- so only the
// same version coming back should exhaust the budget.
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
