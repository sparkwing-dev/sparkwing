//go:build unix

package nodehost

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestFundedBoundKillsUncooperativeSDKTree(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		t.Run(map[bool]string{false: "funding_refused", true: "controller_lost"}[disconnected], func(t *testing.T) {
			entered := make(chan int, 1)
			release := make(chan struct{})
			ready := make(chan struct{})
			bound := make(chan time.Time, 1)
			first := true
			renewed := false
			keeper := lease.Keeper{Lease: time.Minute, Interval: time.Millisecond, Renew: func(ctx context.Context) (lease.Beat, error) {
				if first {
					first = false
					close(ready)
					return lease.Beat{ExpiresAt: time.Now().Add(time.Minute)}, nil
				}
				if renewed {
					return lease.Beat{}, errors.New("controller disconnected")
				}
				select {
				case <-release:
				case <-ctx.Done():
					return lease.Beat{}, ctx.Err()
				}
				expires := time.Now().Add(200 * time.Millisecond)
				bound <- expires
				renewed = true
				return lease.Beat{ExpiresAt: expires, CancelRequested: !disconnected, CancelReason: "out of credits"}, nil
			}}
			ctx, stop := keeper.Run(t.Context())
			defer stop()
			<-ready
			session, err := startProtocolSession(ctx, ProtocolSessionConfig{Command: []string{os.Args[0]}, Environment: testProcessEnv(t, append(os.Environ(), fakeSDKEnv+"=sdk", "GORACE=atexit_sleep_ms=0")...), grace: time.Minute, Log: func(_ context.Context, log nodeproto.FrameLog) error {
				if pid, err := strconv.Atoi(log.Message); err == nil {
					entered <- pid
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			var hello nodeproto.HelloResult
			if err := ask(ctx, session, nodeproto.Request{Op: nodeproto.OpHello, Protocol: 2, Engine: &nodeproto.EngineIdentity{Version: "test"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, &hello); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() {
				_, err := session.Request(ctx, nodeproto.Request{Op: nodeproto.OpRunStep, Job: "grandchild", Step: "body", Attempt: 1, Settled: []string{}})
				finished <- err
			}()
			var pid int
			select {
			case pid = <-entered:
			case err := <-finished:
				t.Fatalf("SDK never started child: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("SDK never started child")
			}
			close(release)
			expires := <-bound
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("SDK ignoring cancellation succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("SDK survived its funded bound")
			}
			if time.Now().Before(expires) {
				t.Fatal("SDK stopped before the hard bound exercised the watchdog")
			}
			assertProcessStopped(t, pid)
			assertProcessStopped(t, session.cmd.Process.Pid)
		})
	}
}

type cleanupPIDLog struct{ pids chan int }

func (l cleanupPIDLog) Write(data []byte) (int, error) {
	if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
		l.pids <- pid
	}
	return len(data), nil
}

func TestFundedBoundKillsUncooperativeCleanupTree(t *testing.T) {
	for _, leaderExits := range []bool{false, true} {
		t.Run(strconv.FormatBool(leaderExits), func(t *testing.T) {
			bound := time.Now().Add(300 * time.Millisecond)
			keeper := lease.Keeper{Lease: time.Minute, Interval: time.Second, Renew: func(context.Context) (lease.Beat, error) {
				return lease.Beat{ExpiresAt: bound, CancelRequested: true}, nil
			}}
			ctx, stop := keeper.Run(t.Context())
			defer stop()
			<-ctx.Done()
			cleanups := &ProcessCleanups{}
			// The shell and its child ignore SIGTERM; only the host's hard kill stops them.
			script := `trap '' TERM; sleep 100 & echo $!; wait`
			if leaderExits {
				script = `trap '' TERM; sleep 100 & echo $!; exit 0`
			}
			if _, err := cleanups.register([]string{"/bin/sh", "-c", script}, ""); err != nil {
				t.Fatal(err)
			}
			pids := make(chan int, 1)
			done := make(chan error, 1)
			go func() {
				done <- cleanups.run(lease.ExecutionContext(ctx), CleanupScope{Env: testProcessEnv(t, os.Environ()...), Log: cleanupPIDLog{pids: pids}})
			}()
			pid := <-pids
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cleanup ignoring cancellation succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cleanup survived its funded bound")
			}
			assertProcessStopped(t, pid)
		})
	}
}

func assertProcessStopped(t *testing.T, pid int) {
	t.Helper()
	processes, err := procgroup.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.PID == pid && !process.Terminated() {
			t.Errorf("descendant %d remains live", pid)
		}
	}
	if errors.Is(t.Context().Err(), context.DeadlineExceeded) {
		t.Fatal("test deadline exceeded")
	}
}
