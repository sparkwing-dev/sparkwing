package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func stuckOlderDaemon(t *testing.T, home, version, buildIdentity string) *atomic.Int64 {
	t.Helper()
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := wingd.ListenSocket(sock)
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
					BuildIdentity:       buildIdentity,
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

func TestTakeoverStopsAfterOneReplacement(t *testing.T) {
	home := shortHome(t)
	handshakes := stuckOlderDaemon(t, home, "v0.1.0", wingwire.BuildIdentity)
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
	for _, want := range []string{"v0.1.0", "v9.9.9", "Stop competing Sparkwing installations", "sparkwing daemon restart", "SPARKWING_HOME"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("takeover failure = %v, want %q", err, want)
		}
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("successor spawns = %d, want %d", got, 1)
	}
	if got := handshakes.Load(); got > 2 {
		t.Fatalf("handshakes = %d; the takeover loop is unbounded", got)
	}
}

func TestTakeoverReplacesSameVersionDaemonWithoutNonBlockingAdmission(t *testing.T) {
	for _, buildIdentity := range []string{"wingwire-v1-liveness", ""} {
		t.Run(buildIdentity, func(t *testing.T) {
			home := shortHome(t)
			handshakes := stuckOlderDaemon(t, home, "v1.0.0", buildIdentity)
			spawns := &atomic.Int64{}

			_, err := EnsureDaemon(context.Background(), Options{
				Home: home, Version: "v1.0.0",
				Spawn:       func(string, string) error { spawns.Add(1); return nil },
				DialTimeout: 200 * time.Millisecond,
				Backoff:     5 * time.Millisecond,
			})
			if !errors.Is(err, errTakeoverExhausted) {
				t.Fatalf("same-version old daemon result = %v, want errTakeoverExhausted", err)
			}
			if got := spawns.Load(); got != 1 {
				t.Fatalf("successor spawns = %d, want %d", got, 1)
			}
			if got := handshakes.Load(); got > 2 {
				t.Fatalf("handshakes = %d; same-version takeover loop is unbounded", got)
			}
		})
	}
}

func TestTakeoverStopsWhenADifferentPredecessorAnswers(t *testing.T) {
	home := shortHome(t)
	sock, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := wingd.ListenSocket(sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	const predecessors = 2
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
				buildIdentity := wingwire.BuildIdentity
				if seen <= predecessors {
					version = fmt.Sprintf("v0.%d.0", seen)
					buildIdentity = ""
				}
				line, _ := wingwire.Encode(&wingwire.HelloAck{
					ProtocolMajor:       wingd.ProtocolMajor,
					NativeProtocolMajor: wingd.ProtocolMajor,
					BinaryVersion:       version,
					BuildIdentity:       buildIdentity,
				})
				if _, err := nc.Write(line); err != nil {
					return
				}
				_, _ = r.read()
			}()
		}
	}()

	spawns := &atomic.Int64{}
	_, err = EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v9.9.9",
		Spawn:       func(string, string) error { spawns.Add(1); return nil },
		DialTimeout: 200 * time.Millisecond,
		Backoff:     5 * time.Millisecond,
	})
	if !errors.Is(err, errTakeoverExhausted) {
		t.Fatalf("different predecessor result = %v, want a version conflict", err)
	}
	if got := spawns.Load(); got != 1 {
		t.Fatalf("successor spawns = %d, want one replacement attempt", got)
	}
}
