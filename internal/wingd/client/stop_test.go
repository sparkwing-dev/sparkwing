package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const stopTestWait = 10 * time.Second

func runDaemon(t *testing.T, home, version string) chan error {
	t.Helper()
	d, err := wingd.New(wingd.Config{Home: home, Version: version})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- d.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			t.Error("daemon did not stop within 1s of cleanup cancellation")
		}
	})
	select {
	case <-d.Ready():
	case err := <-done:
		t.Fatalf("daemon exited before serving: %v", err)
	case <-time.After(stopTestWait):
		t.Fatal("daemon never became ready")
	}
	return done
}

func TestStop_EndsTheDaemonAndLeavesNothingAnswering(t *testing.T) {
	home := shortHome(t)
	done := runDaemon(t, home, "v1.0.0")

	ctx, cancel := context.WithTimeout(context.Background(), stopTestWait)
	defer cancel()
	if err := Stop(ctx, Options{Home: home}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon Run returned %v, want a clean stop", err)
		}
	case <-time.After(stopTestWait):
		t.Fatal("daemon kept running after Stop returned")
	}
	if _, err := Query(ctx, Options{Home: home}); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("query after stop: got %v, want ErrNoDaemon", err)
	}
}

const stopReleaseRounds = 20

func TestStop_ReturnsOnlyAfterTheDaemonReleasedTheHome(t *testing.T) {
	for round := range stopReleaseRounds {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			t.Parallel()

			home := shortHome(t)
			done := runDaemon(t, home, "v1.0.0")

			ctx, cancel := context.WithTimeout(context.Background(), stopTestWait)
			if err := Stop(ctx, Options{Home: home}); err != nil {
				cancel()
				t.Fatalf("stop: %v", err)
			}
			cancel()

			held, err := wingd.LockHeld(home)
			if err != nil {
				t.Fatalf("read election lock: %v", err)
			}
			if held {
				t.Fatalf("stop returned while the daemon still held the home; its final state write can still land under %s", home)
			}
			if err := os.RemoveAll(home); err != nil {
				t.Fatalf("remove home after stop: %v", err)
			}

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("daemon Run returned %v, want a clean stop", err)
				}
			case <-time.After(stopTestWait):
				t.Fatal("daemon kept running after Stop returned")
			}
		})
	}
}

func TestStop_DoesNotStartOneToStopIt(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), stopTestWait)
	defer cancel()

	if err := Stop(ctx, Options{Home: home}); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("stop with no daemon: got %v, want ErrNoDaemon", err)
	}
	if _, err := Query(ctx, Options{Home: home}); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("stop started a daemon: query returned %v, want ErrNoDaemon", err)
	}
}

func TestStop_KeepsANewerBuildFromTakingOverInstead(t *testing.T) {
	home := shortHome(t)
	done := runDaemon(t, home, "v1.0.0")

	ctx, cancel := context.WithTimeout(context.Background(), stopTestWait)
	defer cancel()
	spawned := make(chan struct{}, 1)
	err := Stop(ctx, Options{
		Home:    home,
		Version: "v2.0.0",
		Spawn:   func(string, string) error { spawned <- struct{}{}; return nil },
	})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-spawned:
		t.Fatal("stop spawned a successor daemon")
	default:
	}
	select {
	case <-done:
	case <-time.After(stopTestWait):
		t.Fatal("daemon kept running after Stop returned")
	}
}

func TestProbe_ReportsTheDaemonsOwnVersion(t *testing.T) {
	home := shortHome(t)
	runDaemon(t, home, "v0.0.0")
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), stopTestWait)
	defer cancel()
	info, err := Probe(ctx, sock)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if info.BinaryVersion != "v0.0.0" {
		t.Errorf("probed version %q, want v0.0.0", info.BinaryVersion)
	}
	if info.Socket != sock {
		t.Errorf("probed socket %q, want %q", info.Socket, sock)
	}
	if info.ProtocolMajor != wingd.ProtocolMajor {
		t.Errorf("probed protocol major %d, want %d", info.ProtocolMajor, wingd.ProtocolMajor)
	}
}

func TestProbe_ReportsNoDaemonForAnUnservedSocket(t *testing.T) {
	home := shortHome(t)
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTestWait)
	defer cancel()
	if _, err := Probe(ctx, sock); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("probe of an unserved socket: got %v, want ErrNoDaemon", err)
	}
}
