package client

import (
	"context"
	"errors"
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

func TestRetryStopsAfterItsAttemptBudget(t *testing.T) {
	cause := errors.New("connection reset")
	r := newRetry("queue state", 3)
	for i := 0; i < 2; i++ {
		if err := r.wait(context.Background(), cause); err != nil {
			t.Fatalf("attempt %d ended the retry early: %v", i+1, err)
		}
	}
	err := r.wait(context.Background(), cause)
	if !errors.Is(err, cause) {
		t.Fatalf("exhausted retry = %v, want it to carry the transport failure", err)
	}
	if !strings.Contains(err.Error(), "queue state") || !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("exhausted retry = %q, want it to name the exchange and the attempt count", err)
	}
}

func TestRetryPacesItsAttempts(t *testing.T) {
	r := newRetry("acquire", 0)
	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := r.wait(context.Background(), errors.New("boom")); err != nil {
			t.Fatalf("unbounded retry gave up: %v", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("four retries took %s; an unpaced loop is what spun a core", elapsed)
	}
	r.reset()
	if r.delay != retryBaseDelay || r.attempts != 0 {
		t.Fatalf("reset left delay %s attempts %d, want the base pacing back", r.delay, r.attempts)
	}
}

func TestRetryReturnsOnceTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := newRetry("acquire", 0)
	start := time.Now()
	if err := r.wait(ctx, errors.New("boom")); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry under a cancelled context = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("cancelled retry still waited %s", elapsed)
	}
}

// failingDaemon accepts, answers the handshake, and then closes every
// connection without answering the request on it. It is the daemon shape
// that turned a client retry loop into a spin: each dropped connection
// costs the daemon a state fsync, so an unpaced client burns a core on
// each side of the socket.
func failingDaemon(t *testing.T, home string) *atomic.Int64 {
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

	accepted := &atomic.Int64{}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				defer nc.Close()
				r := newFrameReader(nc)
				if _, err := r.read(); err != nil {
					return
				}
				line, _ := wingwire.Encode(&wingwire.HelloAck{
					ProtocolMajor:       wingd.ProtocolMajor,
					NativeProtocolMajor: wingd.ProtocolMajor,
					BinaryVersion:       "v9.9.9",
				})
				if _, err := nc.Write(line); err != nil {
					return
				}
				_, _ = r.read()
			}()
		}
	}()
	return accepted
}

// TestQueueStateDoesNotSpinAgainstAFailingDaemon is the client half of the
// reported spin: a daemon that drops every exchange must cost a handful of
// attempts in a window, not as many as the socket will carry.
func TestQueueStateDoesNotSpinAgainstAFailingDaemon(t *testing.T) {
	home := shortHome(t)
	accepted := failingDaemon(t, home)

	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       func(string, string) error { return errors.New("no spawn in this test") },
		DialTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if _, err := cl.QueueState(ctx); err == nil {
		t.Fatal("queue state against a daemon that answers nothing returned success")
	}

	if got := accepted.Load(); got > 8 {
		t.Fatalf("client made %d connections in 400ms; the retry loop is unpaced", got)
	}
}

// TestQueueStateGivesUpRatherThanRetryingForever bounds the read-only
// path. Nothing depends on a status read eventually succeeding, so a
// daemon that keeps failing it must be reported, not asked forever.
func TestQueueStateGivesUpRatherThanRetryingForever(t *testing.T) {
	home := shortHome(t)
	failingDaemon(t, home)

	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       func(string, string) error { return errors.New("no spawn in this test") },
		DialTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()

	done := make(chan error, 1)
	go func() {
		_, qerr := cl.QueueState(context.Background())
		done <- qerr
	}()
	select {
	case qerr := <-done:
		if qerr == nil {
			t.Fatal("queue state returned success from a daemon that answered nothing")
		}
		if !strings.Contains(qerr.Error(), "attempts") {
			t.Fatalf("queue state failure = %v, want it to report the exhausted attempts", qerr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("queue state never gave up on a daemon that answers nothing")
	}
}
