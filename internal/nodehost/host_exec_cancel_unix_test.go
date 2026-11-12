//go:build unix

package nodehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func TestHostCommandCancellationJoinsEscapedChildren(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "hold"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	const children = 16
	req := nodewire.ExecRequest{
		ID: "cancel", Dir: dir, Capture: true,
		Argv: []string{"sh", "-c", `exec 3<> "$D/ready"
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16; do
timeout 60 sh -c 'echo $$ > "$D/$1.pid"; echo ready > "$D/ready"; read line < "$D/hold"' sh "$i" >/dev/null 2>&1 &
done
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16; do read -r line <&3; done
echo ready; wait`},
		Env: []string{"PATH=" + os.Getenv("PATH"), "D=" + dir},
	}
	kill := make(chan struct{})
	ready := 0
	_, session := runHostCommand(req, nil, kill, t.Context().Done(), func(stream string, chunk []byte, _ bool) error {
		if stream == "stdout" {
			ready += bytes.Count(chunk, []byte{'\n'})
			if ready == 1 {
				close(kill)
				ready++
			}
		}
		return nil
	}, nil)
	t.Cleanup(func() {
		if err := endCommandSessions([]*commandSession{session}); err != nil {
			t.Error(err)
		}
	})
	for i := 1; i <= children; i++ {
		path := filepath.Join(dir, strconv.Itoa(i)+".pid")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		processes, err := procgroup.ListSessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range processes {
			if process.PID == pid && !process.Terminated() {
				t.Errorf("cancellation returned with escaped child still alive: %+v", process)
			}
		}
	}
}

//nolint:contextcheck // Rescue and teardown outlive the test deadline; session inspection has its own bounds.
func TestHostCommandReportsSessionCleanupFailure(t *testing.T) {
	for _, trigger := range []string{"cancellation", "sink", "capture", "nonzero"} {
		t.Run(trigger, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			t.Cleanup(cancel)
			kill := make(chan struct{})
			pid := 0
			var restore func()
			t.Cleanup(func() {
				if restore != nil {
					restore()
				}
			})
			start := func(cmd *exec.Cmd, env []string) (*commandSession, error) {
				session, err := startHostCommand(cmd, env)
				if err != nil {
					return nil, err
				}
				identity := session.identity
				rescueErr := make(chan error, 1)
				stopRescue := context.AfterFunc(ctx, func() { rescueErr <- procgroup.KillSession(identity) })
				// safety: the injected invalid identity makes the real session-cleanup call fail before any signal.
				session.identity.SessionID = 0
				if trigger == "capture" {
					session.identity = identity
					session.captured = false
					session.captureErr = errors.New("injected SDK command session capture failure")
				}
				restore = func() {
					if !stopRescue() {
						if err := <-rescueErr; err != nil {
							t.Error(err)
						}
					}
					session.identity = identity
					session.captured, session.captureErr = true, nil
					if err := endCommandSessions([]*commandSession{session}); err != nil {
						t.Error(err)
					}
				}
				return session, nil
			}
			dir := t.TempDir()
			holdPath := filepath.Join(dir, "leader-hold")
			if err := unix.Mkfifo(holdPath, 0o600); err != nil {
				t.Fatal(err)
			}
			hold, err := os.OpenFile(holdPath, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := hold.Close(); err != nil {
					t.Error(err)
				}
			})
			request := nodewire.ExecRequest{
				ID: "cleanup-failure", Dir: dir, Env: append(os.Environ(), "HOSTEXEC_LEADER_HOLD="+holdPath), Capture: true,
				Argv: []string{"sh", "-c", `sleep 300 & echo $!; read line < "$HOSTEXEC_LEADER_HOLD"; exit 0`},
			}
			if trigger == "nonzero" {
				request.Argv[2] = strings.Replace(request.Argv[2], "exit 0", "exit 7", 1)
			}
			done, session := runHostCommandStarted(request, nil, kill, ctx.Done(), func(stream string, chunk []byte, _ bool) error {
				if stream == "stdout" && len(chunk) > 0 && pid == 0 {
					var err error
					pid, err = strconv.Atoi(strings.TrimSpace(string(chunk)))
					if err != nil {
						return err
					}
					if trigger != "sink" {
						close(kill)
					}
					// safety: cancellation is observable before the leader may exit,
					// so the callback cannot lose the cleanup request.
					if _, err := hold.WriteString("\n"); err != nil {
						return err
					}
					if trigger == "sink" {
						return os.ErrClosed
					}
				}
				return nil
			}, nil, start)
			processes, err := procgroup.ListSessions()
			if err != nil {
				t.Fatal(err)
			}
			alive := false
			for _, process := range processes {
				if process.PID == pid {
					alive = !process.Terminated()
					t.Logf("after return: child=%+v result=%+v", process, done)
				}
			}
			want := "invalid guarded session identity"
			if trigger == "capture" {
				want = "injected SDK command session capture failure"
				if err := endCommandSessions([]*commandSession{session}); err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("session close lost capture error: %v", err)
				}
			} else if !alive {
				t.Fatal("probe did not keep the failed-cleanup child alive across return")
			}
			if trigger == "nonzero" && !strings.Contains(done.StartError, "exit status 7") {
				t.Errorf("cleanup error replaced command failure: %+v", done)
			}
			if !strings.Contains(done.StartError, want) {
				t.Errorf("session cleanup error lost: %+v", done)
			}
		})
	}
}
