package client

import (
	"context"
	"crypto/sha256"
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

func TestTakeoverFindsPreTokenDaemonAndJoinsBeforeSpawn(t *testing.T) {
	home := shortHome(t)
	current, err := wingd.EnsureSocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	// hack: upgrades must find v0.67 sockets, which lack the per-home token.
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(home)))[:12]
	dir := filepath.Base(filepath.Dir(current))
	prefix := dir[:strings.Index(dir, hash)]
	legacy := filepath.Join(filepath.Dir(filepath.Dir(current)), prefix+hash, "d.sock")
	lock, err := wingd.AcquireControllerLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ln, err := wingd.ListenSocket(legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer nc.Close()
		r := newFrameReader(nc)
		_, err = r.read()
		if err == nil {
			err = writeTestFrame(nc, &wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "v0.67.0"})
		}
		if err == nil {
			var message wingwire.Message
			message, err = r.read()
			if _, ok := message.(*wingwire.DrainRequest); !ok && err == nil {
				err = fmt.Errorf("expected drain, got %T", message)
			}
		}
		if err == nil {
			err = writeTestFrame(nc, &wingwire.DrainAck{})
		}
		_ = ln.Close()
		_ = lock.Close()
		done <- err
	}()
	defer func() {
		_ = ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	spawns := 0
	var messages strings.Builder
	spawn := spawnInProcess(t, home)
	cl, err := EnsureDaemon(ctx, Options{
		Home: home, Version: "v1.0.0",
		Spawn: func(h, v string) error {
			spawns++
			if held, err := wingd.LockHeld(home); err != nil || held {
				t.Fatalf("spawn before predecessor joined: held=%t error=%v", held, err)
			}
			return spawn(h, v)
		},
		Logf: func(format string, args ...any) { fmt.Fprintf(&messages, format, args...) },
	})
	if err != nil {
		t.Fatalf("upgrade startup: %v", err)
	}
	defer cl.Close()
	if spawns != 1 || cl.DaemonVersion() != "v1.0.0" || !strings.Contains(messages.String(), "v0.67.0") {
		t.Fatalf("upgrade: spawns=%d version=%q messages=%q", spawns, cl.DaemonVersion(), messages.String())
	}
}

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

func TestTakeoverMissingSuccessorDoesNotSpawnAgain(t *testing.T) {
	home := shortHome(t)
	runDaemon(t, home, "v0.67.0")
	spawns := 0
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := EnsureDaemon(ctx, Options{
		Home: home, Version: "v0.68.3", Backoff: time.Nanosecond,
		Spawn: func(string, string) error { spawns++; return nil },
	})
	if !errors.Is(err, errDaemonUnreachable) || spawns != 1 {
		t.Fatalf("missing successor: error=%v spawns=%d, want one replacement", err, spawns)
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

func TestTakeoverWaitsForDrainingPredecessor(t *testing.T) {
	for _, protocol := range []int{wingd.ProtocolMajor, wingd.ProtocolMajor - 1} {
		t.Run(fmt.Sprintf("protocol-%d", protocol), func(t *testing.T) {
			home := shortHome(t)
			sock, err := wingd.EnsureSocketPath(home)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
				t.Fatal(err)
			}
			ln, err := wingd.ListenSocket(sock)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				for step := range 3 {
					nc, err := ln.Accept()
					if err != nil {
						done <- err
						return
					}
					err = func() error {
						defer nc.Close()
						r := newFrameReader(nc)
						if _, err := r.read(); err != nil {
							return err
						}
						ack := &wingwire.HelloAck{
							ProtocolMajor: protocol, NativeProtocolMajor: protocol,
							BinaryVersion: "v0.1.0", BuildIdentity: wingwire.BuildIdentity,
							Draining: step == 1,
						}
						if step == 2 {
							ack.ProtocolMajor, ack.NativeProtocolMajor = wingd.ProtocolMajor, wingd.ProtocolMajor
							ack.BinaryVersion = "v9.9.9"
						}
						line, err := wingwire.Encode(ack)
						if err != nil {
							return err
						}
						if _, err := nc.Write(line); err != nil {
							return err
						}
						if step == 0 {
							request, err := r.read()
							if err != nil {
								return err
							}
							if _, ok := request.(*wingwire.DrainRequest); !ok {
								return fmt.Errorf("expected drain request, got %T", request)
							}
							line, err := wingwire.Encode(&wingwire.DrainAck{})
							if err != nil {
								return err
							}
							_, err = nc.Write(line)
							return err
						}
						_, _ = r.read()
						return nil
					}()
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			t.Cleanup(func() { _ = ln.Close(); <-done })
			spawns := 0
			cl, err := EnsureDaemon(t.Context(), Options{
				Home: home, Version: "v9.9.9",
				Spawn: func(string, string) error { spawns++; return nil },
			})
			if err != nil {
				t.Fatalf("draining predecessor prevented replacement: %v", err)
			}
			defer cl.Close()
			if spawns != 1 || cl.DaemonVersion() != "v9.9.9" {
				t.Fatalf("replacement: spawns=%d version=%q", spawns, cl.DaemonVersion())
			}
		})
	}
}
