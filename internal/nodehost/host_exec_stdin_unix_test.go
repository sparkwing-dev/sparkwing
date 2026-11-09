//go:build unix

package nodehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

// A sink slower than the drain grace must still receive every byte the exited command wrote.
func TestHostCommandKeepsBufferedOutputPastASlowSink(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		name := "complete"
		if cancelWait {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req := nodewire.ExecRequest{ID: "1", Argv: []string{"head", "-c", "12288", "/dev/zero"}, Dir: t.TempDir(), Env: []string{"PATH=" + os.Getenv("PATH")}}
			entered := make(chan struct{})
			var once sync.Once
			var got int
			var sinkErr error
			sink := func(_ string, chunk []byte, _ bool) error {
				if len(chunk) > 0 {
					got += len(chunk)
					once.Do(func() { close(entered) })
					select {
					case <-ctx.Done():
						sinkErr = ctx.Err()
						return sinkErr
					// sleepcheck:external-boundary real OS pipe deadline; synctest cannot model the sink mutex
					case <-time.After(2 * commandDrainGrace):
					}
				}
				return nil
			}
			finished := make(chan nodewire.ExecDone, 1)
			go func() {
				done, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), sink, nil)
				if err := endCommandSessions([]*commandSession{session}); err != nil {
					t.Error(err)
				}
				finished <- done
			}()
			bound, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			select {
			case <-entered:
			case <-bound.Done():
				t.Fatal("command output never reached the sink")
			}
			if cancelWait {
				cancel()
			}
			select {
			case done := <-finished:
				if cancelWait {
					if !errors.Is(sinkErr, context.Canceled) {
						t.Fatalf("sink wait returned %v after cancellation", sinkErr)
					}
				} else if done.ExitCode != 0 || got != 12288 {
					t.Fatalf("captured %d of 12288 bytes, exit %d", got, done.ExitCode)
				}
			case <-bound.Done():
				t.Fatal("command did not finish after the sink wait")
			}
		})
	}
}

// A background child that holds stdin without reading must not hold the command open.
func TestHostCommandReturnsWhenABackgroundChildHoldsStdin(t *testing.T) {
	dir := t.TempDir()
	pidFile := dir + "/held.pid"
	req := nodewire.ExecRequest{
		ID: "1", Argv: []string{"sh", "-c", `exec 3<&0; sleep 30 <&3 & echo $! > "$P"; exit 0`}, Dir: dir,
		Env: []string{"PATH=" + os.Getenv("PATH"), "P=" + pidFile}, Stdin: bytes.Repeat([]byte("x"), 256<<10),
	}
	finished := make(chan *commandSession, 1)
	go func() {
		_, session := runHostCommand(req, nil, make(chan struct{}), make(chan struct{}), func(string, []byte, bool) error { return nil }, nil)
		finished <- session
	}()
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case session := <-finished:
		if err := endCommandSessions([]*commandSession{session}); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the command never finished while a background child held its stdin")
	}
}
