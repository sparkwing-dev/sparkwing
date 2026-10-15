package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type foregroundRuntimeFake struct {
	wingd.LocalRuntime
	run func(context.Context, wingd.LocalStartOptions) error
}

func (f foregroundRuntimeFake) Run(ctx context.Context, options wingd.LocalStartOptions) error {
	return f.run(ctx, options)
}

func TestWingdRunForwardsForegroundOwnership(t *testing.T) {
	home := filepath.Join(t.TempDir(), "private home")
	defaults := wingd.LocalStartOptions{Home: t.TempDir(), Executable: filepath.Join(t.TempDir(), "sparkwing"), Version: "2.0.0"}
	var got wingd.LocalStartOptions
	failure := errors.New("runtime stopped")
	key := struct{}{}
	ctx := context.WithValue(t.Context(), key, "caller")
	err := runWingdRunCommand(ctx, []string{"--home", home, "--stay", "--version", "2.1.0", "-o", "json"}, &bytes.Buffer{}, defaults,
		func(context.Context) (wingd.LocalRuntime, error) {
			return foregroundRuntimeFake{run: func(runCtx context.Context, options wingd.LocalStartOptions) error {
				if runCtx.Value(key) != "caller" {
					t.Fatal("runtime lost the foreground context")
				}
				got = options
				return failure
			}}, nil
		})
	want := defaults
	want.Home, want.Stay, want.Version = home, true, "2.1.0"
	if !errors.Is(err, failure) || !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %+v, err = %v", got, err)
	}
}

func TestWingdRunWaitsForCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, done := make(chan struct{}), make(chan error, 1)
	options := wingd.LocalStartOptions{Home: t.TempDir()}
	go func() {
		done <- runWingdRunCommand(ctx, []string{"--stay"}, &bytes.Buffer{}, options,
			func(context.Context) (wingd.LocalRuntime, error) {
				return foregroundRuntimeFake{run: func(runCtx context.Context, _ wingd.LocalStartOptions) error {
					close(entered)
					<-runCtx.Done()
					return runCtx.Err()
				}}, nil
			})
	}()
	<-entered
	select {
	case err := <-done:
		t.Fatalf("foreground runtime returned before shutdown: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v", err)
	}
}

func TestWingdRunValidatesBeforeRuntime(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown"}, {"extra"}, {"--home="}, {"--standalone-reason="}, {"--version="}, {"--output", "yaml"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			called := false
			err := runWingdRunCommand(t.Context(), args, &bytes.Buffer{}, wingd.LocalStartOptions{}, func(context.Context) (wingd.LocalRuntime, error) {
				called = true
				return nil, nil
			})
			var usage *clisurface.UsageError
			if !errors.As(err, &usage) || called {
				t.Fatalf("invalid invocation reached runtime: called=%v err=%v", called, err)
			}
		})
	}
}

func TestWingdRunRefusesUnrepresentedRuntimeOptions(t *testing.T) {
	for _, flag := range []string{"--budget=50%", "--headroom=0.2", "--state-db=private.db"} {
		called := false
		err := runWingdRunCommand(t.Context(), []string{flag}, &bytes.Buffer{}, wingd.LocalStartOptions{}, func(context.Context) (wingd.LocalRuntime, error) {
			called = true
			return nil, nil
		})
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.Unavailable || called {
			t.Fatalf("%s was silently discarded: called=%v err=%v", flag, called, err)
		}
	}
}

func TestWingdRunHelpAndCancellationAvoidRuntime(t *testing.T) {
	for _, help := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var output bytes.Buffer
		args := []string{}
		if help {
			args = []string{"--help", "-o", "json"}
		}
		called := false
		err := runWingdRunCommand(ctx, args, &output, wingd.LocalStartOptions{}, func(context.Context) (wingd.LocalRuntime, error) {
			called = true
			return nil, nil
		})
		if called || (help && (err != nil || output.Len() == 0)) || (!help && !errors.Is(err, context.Canceled)) {
			t.Fatalf("help=%v called=%v output=%s err=%v", help, called, &output, err)
		}
	}
}

func TestWingdRunRejectsMissingRuntimeAndKeepsDefaults(t *testing.T) {
	defaults := wingd.LocalStartOptions{Home: t.TempDir(), Version: "2.0.0", Executable: filepath.Join(t.TempDir(), "sparkwing")}
	for _, absent := range []bool{false, true} {
		var got wingd.LocalStartOptions
		err := runWingdRunCommand(t.Context(), []string{"--stay=false", "--standalone-reason", "CI"}, &bytes.Buffer{}, defaults, func(context.Context) (wingd.LocalRuntime, error) {
			if absent {
				return nil, nil
			}
			return foregroundRuntimeFake{run: func(_ context.Context, options wingd.LocalStartOptions) error {
				got = options
				return nil
			}}, nil
		})
		if absent {
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != apierr.Unavailable {
				t.Fatalf("missing runtime error = %v", err)
			}
			continue
		}
		want := defaults
		want.Standalone = true
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("defaults changed: %+v err=%v", got, err)
		}
	}
}

func TestWingdRunChecksCancellationAfterConstruction(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	called := false
	err := runWingdRunCommand(ctx, nil, &bytes.Buffer{}, wingd.LocalStartOptions{Home: t.TempDir()}, func(context.Context) (wingd.LocalRuntime, error) {
		cancel()
		return foregroundRuntimeFake{run: func(context.Context, wingd.LocalStartOptions) error { called = true; return nil }}, nil
	})
	if called || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled construction started a runtime: called=%v err=%v", called, err)
	}
}

func TestWingdRunResolvesHomeWithoutChangingEnvironment(t *testing.T) {
	ambient := t.TempDir()
	t.Setenv("SPARKWING_HOME", ambient)
	for _, args := range [][]string{nil, {"--home", "relative-home"}} {
		var got wingd.LocalStartOptions
		err := runWingdRunCommand(t.Context(), args, &bytes.Buffer{}, wingd.LocalStartOptions{Version: "2.0.0"}, func(context.Context) (wingd.LocalRuntime, error) {
			return foregroundRuntimeFake{run: func(_ context.Context, options wingd.LocalStartOptions) error { got = options; return nil }}, nil
		})
		want := ambient
		if args != nil {
			want, _ = filepath.Abs("relative-home")
		}
		if err != nil || got.Home != want || !filepath.IsAbs(got.Executable) || os.Getenv("SPARKWING_HOME") != ambient {
			t.Fatalf("home=%q executable=%q err=%v", got.Home, got.Executable, err)
		}
	}
}

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
	err := runWingdRunWith([]string{"--home", home, "--version", "test"}, func(ctx context.Context, got string) (wingd.LocalController, error) {
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
