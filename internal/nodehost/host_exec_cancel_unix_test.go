//go:build unix

package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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
			var observed *exec.Cmd
			var restore func()
			t.Cleanup(func() {
				if restore != nil {
					restore()
				}
			})
			start := func(cmd *exec.Cmd, env []string) (*commandSession, error) {
				observed = cmd
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
					if err := endCommandSessions([]*commandSession{session}); !errors.Is(err, session.cleanupErr) {
						t.Errorf("attempt cleanup failure was not retained: %v", err)
					}
					if empty, err := procgroup.SessionEmpty(identity); err != nil || !empty {
						t.Errorf("fixture session survived teardown: empty=%t error=%v", empty, err)
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
				ID: "cleanup-failure", Dir: dir, Env: append(os.Environ(), "HOSTEXEC_LEADER_HOLD="+holdPath), Capture: false,
				Argv: []string{"sh", "-c", `sleep 300 & printf cleanup-stderr >&2; echo $!; read line < "$HOSTEXEC_LEADER_HOLD"; exit 0`},
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
			if trigger == "nonzero" && done.ExitCode != 7 {
				t.Errorf("cleanup error replaced command failure: %+v", done)
			}
			if done.StartError != "" || observed == nil || observed.ProcessState == nil || done.ExitCode != observed.ProcessState.ExitCode() || done.Signaled != session.signaled(observed) {
				t.Errorf("observed command status lost: result=%+v state=%v", done, observed.ProcessState)
			}
			if string(done.Stdout) != strconv.Itoa(pid)+"\n" || string(done.Stderr) != "cleanup-stderr" {
				t.Errorf("command tails lost: %+v", done)
			}
			if err := session.failure(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("host cleanup error lost: %v", err)
			}
		})
	}
}

type commandCleanupAttemptProcess struct {
	ExecutorProcess
	commands *protocolSession
}

func (p *commandCleanupAttemptProcess) Close() error {
	return errors.Join(p.ExecutorProcess.Close(), p.commands.Close())
}

//nolint:contextcheck // Failed-cleanup rescue must remain effective after the test deadline.
func TestHostCommandCleanupFailureOverridesSDKSuccess(t *testing.T) {
	for _, lang := range []string{"go", "typescript"} {
		for _, exit := range []int{0, 7} {
			t.Run(fmt.Sprintf("%s/exit=%d", lang, exit), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				t.Cleanup(cancel)
				dir := t.TempDir()
				holdPath := filepath.Join(dir, "hold")
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
				h := startHostExecHarness(t, lang, dir)
				ready := make(chan struct{})
				var command *commandSession
				var identity procgroup.SessionIdentity
				var stopRescue func() bool
				rescueErr := make(chan error, 1)
				h.session.mu.Lock()
				h.session.startCommand = func(cmd *exec.Cmd, env []string) (*commandSession, error) {
					cmd.Args = []string{"sh", "-c", fmt.Sprintf(`sleep 300 & printf cleanup-out; printf cleanup-err >&2; read line < "$HOSTEXEC_LEADER_HOLD"; exit %d`, exit)}
					cmd.Env = append(cmd.Env, "HOSTEXEC_LEADER_HOLD="+holdPath)
					session, err := startHostCommand(cmd, env)
					if err != nil {
						return nil, err
					}
					command, identity = session, session.identity
					stopRescue = context.AfterFunc(ctx, func() { rescueErr <- procgroup.KillSession(identity) })
					session.identity.SessionID = 0
					close(ready)
					return session, nil
				}
				h.session.mu.Unlock()
				t.Cleanup(func() {
					if command != nil {
						if !stopRescue() {
							if err := <-rescueErr; err != nil {
								t.Error(err)
							}
						}
						command.identity = identity
						_ = h.session.Close()
						if empty, err := procgroup.SessionEmpty(identity); err != nil || !empty {
							t.Errorf("fixture session remained: empty=%t error=%v", empty, err)
						}
					}
				})
				type answer struct {
					reply nodewire.Reply
					err   error
				}
				returned := make(chan answer, 1)
				go func() {
					reply, err := h.session.Request(ctx, nodewire.Request{ID: "cleanup-step", Op: nodewire.OpRunStep, Job: "job", Step: "captured", Attempt: 1, Settled: []string{}})
					returned <- answer{reply, err}
				}()
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("fixture command did not start")
				}
				h.session.mu.Lock()
				count := len(h.session.execs)
				for _, pending := range h.session.execs {
					pending.stop()
				}
				h.session.mu.Unlock()
				if count != 1 {
					t.Fatalf("running commands=%d, want 1", count)
				}
				if _, err := hold.WriteString("\n"); err != nil {
					t.Fatal(err)
				}
				var response answer
				select {
				case response = <-returned:
				case <-ctx.Done():
					t.Fatal("SDK step did not return")
				}
				if response.err != nil {
					t.Fatal(response.err)
				}
				var result nodewire.RunResult
				if err := response.reply.Decode(&result); err != nil {
					t.Fatal(err)
				}
				if result.Outcome != "success" {
					t.Fatalf("fixture SDK did not swallow its command error: %+v", result)
				}
				var observation hostExecObservation
				if err := json.Unmarshal(result.Output, &observation); err != nil {
					t.Fatal(err)
				}
				if observation.Exit != exit || observation.Stdout != "cleanup-out" || observation.Stderr != "cleanup-err" || observation.Terminated != "" || (observation.Error != "") != (exit != 0) || strings.Contains(observation.Error, "failed to start") {
					t.Fatalf("SDK lost observed command result: %+v", observation)
				}
				// safety: even a successful retry must leave the original host failure owned by the attempt.
				command.identity = identity
				input, store, deps, _ := executorFixture(t)
				start := deps.Start
				deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
					process, err := start(ctx, in, activity)
					if err != nil {
						return nil, err
					}
					return &commandCleanupAttemptProcess{ExecutorProcess: process, commands: h.session}, nil
				}
				executor, err := NewNodeExecutor(deps)
				if err != nil {
					t.Fatal(err)
				}
				err = executor.Execute(ctx, input)
				if err == nil || !strings.Contains(err.Error(), "invalid guarded session identity") || store.changes[len(store.changes)-1].To != runstate.NodeFailed {
					t.Fatalf("SDK success bypassed host failure: error=%v changes=%+v", err, store.changes)
				}
			})
		}
	}
}

func TestHostCommandAlreadyGoneCleanupIsBenign(t *testing.T) {
	req := nodewire.ExecRequest{ID: "gone", Dir: t.TempDir(), Env: os.Environ(), Capture: true, Argv: []string{"sh", "-c", "exit 0"}}
	done, session := runHostCommand(req, nil, nil, nil, func(string, []byte, bool) error { return nil }, nil)
	if done.StartError != "" || session.failure() != nil || done.ExitCode != 0 {
		t.Fatalf("normal result: %+v", done)
	}
	if err := session.kill(); err != nil {
		t.Fatalf("captured session already gone: %v", err)
	}
	session.captured = false
	if err := session.kill(); err != nil {
		t.Fatalf("fallback ESRCH: %v", err)
	}
	if err := endCommandSessions([]*commandSession{session}); err != nil {
		t.Fatal(err)
	}
}
