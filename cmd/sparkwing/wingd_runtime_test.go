package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestWingdRunRequiresControllerBeforeOpeningHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "controller")
	err := runWingdRunWith([]string{"--home", home, "--version", "test"}, nil)
	if err == nil || !strings.Contains(err.Error(), "controller composition") {
		t.Fatalf("run without controller = %v", err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home was opened without a controller: %v", err)
	}
}

func TestWingdRunOpensControllerUnderElection(t *testing.T) {
	home := filepath.Join(t.TempDir(), "controller")
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	want := errors.New("controller unavailable")
	called := false
	err := runWingdRunWith([]string{"--home", home, "--version", "test", "--stay"}, func(ctx context.Context, got string) (wingd.LocalController, error) {
		called = true
		if got != home || ctx.Err() != nil {
			t.Fatalf("factory home = %q, context = %v", got, ctx.Err())
		}
		held, err := wingd.LockHeld(home)
		if err != nil || !held {
			t.Fatalf("election before factory = %v, %v", held, err)
		}
		return nil, want
	})
	if !called || !errors.Is(err, want) {
		t.Fatalf("factory called = %v, run = %v", called, err)
	}
	held, err := wingd.LockHeld(home)
	if err != nil || held {
		t.Fatalf("election after failure = %v, %v", held, err)
	}
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admission socket opened after factory failure: %v", err)
	}
}
