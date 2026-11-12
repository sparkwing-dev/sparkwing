//go:build linux

package cluster

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerShutdownPreservesFailures(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_SHUTDOWN_FAILURE") == "1" {
		command, mode := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
			if mode == "two-units" {
				firstObserved := make(chan struct{})
				claims := 0
				loop := runners.Loop{
					Config:       runners.LoopConfig{Version: "v1.2.3", MaxConcurrent: 2, MaxClaims: 2},
					CheckVersion: func(context.Context, string) error { return nil },
					Controller: cliWire{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						claims++
						return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: strconv.Itoa(claims), RunID: "run", Capability: "private"}}, nil
					}},
					OnError: func(err error) {
						if errors.Is(err, context.Canceled) {
							close(firstObserved)
						}
					},
					Host: cliHost(func(_ context.Context, unit controllerwire.ClaimedUnit) error {
						if unit.UnitID == "1" {
							<-ctx.Done()
							return context.Canceled
						}
						if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
							t.Fatal(err)
						}
						<-firstObserved
						return errors.New("second unit cleanup failed")
					}),
				}
				return runners.RuntimeConfig{}, loop.Run(ctx)
			}
			socket := fmt.Sprintf("@sw3506-source-%d", os.Getpid())
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			script := "#!/bin/sh\nexec " + quote(binary) + " -test.run=^TestSourceGitExternalKillHelper$ -- " + quote(socket) + "\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			inherited := &acquireExternalKillContext{Context: ctx, listener: listener}
			_, err = source.Acquire(inherited, source.Spec{RepoKey: "local:fixture", LocalCheckout: t.TempDir()}, source.Options{ //nolint:contextcheck // The inherited wrapper confirms external child death before shutdown.
				Engines: struct{ source.EngineResolver }{}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"},
				Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, GitAccess: source.GitAccessOwner, BuildEnvironment: sourcetest.BuildEnvironment,
			})
			return runners.RuntimeConfig{}, err
		}}
		os.Args = []string{"sparkwing-runner", command, "--config=/fixture/config.yaml"}
		if command == "warm" {
			os.Args = []string{"sparkwing-runner", command, "--controller=" + url, "--credentials-dir=" + credentials}
		}
		mainWithRuntime("v1.2.3", clusterRuntime{Runner: cli})
		return
	}
	for _, command := range []string{"start", "warm"} {
		for _, mode := range []string{"two-units", "acquire-external"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerShutdownPreservesFailures$", "--", command, mode)
				child.Env = append(os.Environ(), "SPARKWING_TEST_SHUTDOWN_FAILURE=1")
				output, err := child.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("shutdown hid %s failure: %v %s", mode, err, output)
				}
				want := "second unit cleanup failed"
				if mode == "acquire-external" {
					want = "source git rev-parse failed: signal: killed"
				}
				if !bytes.Contains(output, []byte(want)) {
					t.Fatalf("missing original failure %q: %s", want, output)
				}
			})
		}
	}
}

type acquireExternalKillContext struct {
	context.Context
	listener *net.UnixListener
	once     sync.Once
	checks   atomic.Uint32
}

func (c *acquireExternalKillContext) Done() <-chan struct{} {
	// safety: exec.Cmd checks cancellation before spawning; only its post-start check can await the Git helper.
	if c.checks.Add(1) == 1 {
		return c.Context.Done()
	}
	c.once.Do(func() {
		conn, err := c.listener.AcceptUnix()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			panic(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			panic(err)
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			panic(err)
		}
		for {
			var info unix.Siginfo
			err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				panic(err)
			}
			break
		}
		fmt.Fprintln(os.Stderr, "source Git external SIGKILL confirmed before SIGTERM")
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			panic(err)
		}
		<-c.Context.Done()
	})
	return c.Context.Done()
}

func TestSourceGitExternalKillHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	conn, err := net.Dial("unix", os.Args[len(os.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Read(make([]byte, 1))
}
