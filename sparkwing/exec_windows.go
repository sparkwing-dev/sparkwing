//go:build windows

package sparkwing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func configureProcessGroup(_ context.Context, cmd *exec.Cmd, _ <-chan struct{}) {
	if len(cmd.Args) != 3 || cmd.Args[1] != "-c" {
		return
	}
	name := filepath.Base(cmd.Path)
	if !strings.EqualFold(name, "bash") && !strings.EqualFold(name, "bash.exe") {
		return
	}
	// bug: Git Bash truncates long -c arguments from Windows; an environment value preserves the program and stdin.
	const variable = "SPARKWING_BASH_PROGRAM"
	const limit = 32767 - len(variable) - 2
	program := cmd.Args[2]
	if len(utf16.Encode([]rune(program))) > limit {
		cmd.Err = fmt.Errorf("Windows Bash program exceeds environment limit of %d UTF-16 code units", limit)
		return
	}
	cmd.Args[2] = `eval 'unset ` + variable + `;' "$` + variable + `"`
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, variable+"="+program)
}

func commandResourceUsage(*exec.Cmd) (time.Duration, int64, bool) { return 0, 0, false }

// stepJob is the kill-on-close Job Object one step command runs inside. It
// is what a timeout terminates and what a sweep can open by name once this
// process is gone.
type stepJob struct {
	job       *procgroup.Job
	cancelled *atomic.Bool
	cmd       *exec.Cmd
}

// safety: a distinct status distinguishes our termination from a natural exit during cancellation.
const stepCancellationExitCode uint32 = 0x53574341

func (j stepJob) wasCancelled() bool {
	if j.cancelled == nil || !j.cancelled.Load() {
		return false
	}
	return j.job == nil || (j.cmd.ProcessState != nil && uint32(j.cmd.ProcessState.ExitCode()) == stepCancellationExitCode)
}

func stepJobName() string {
	var suffix [8]byte
	_, _ = rand.Read(suffix[:])
	run := strings.Map(func(r rune) rune {
		if r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, os.Getenv("SPARKWING_RUN_ID"))
	if run == "" {
		run = "local"
	}
	return `Local\sparkwing-step-` + run + "-" + hex.EncodeToString(suffix[:])
}

// startStepCommand starts the command inside its own job so a cancelled or
// timed-out step ends everything it spawned, not only its leader. A job
// that cannot be created is not fatal: the command still runs, unowned, and
// the debug log says so.
func startStepCommand(cmd *exec.Cmd, display string) (stepJob, error) {
	cancelled := &atomic.Bool{}
	trackCancellation := func(cancel func() error) {
		cmd.Cancel = func() error {
			err := cancel()
			if err == nil {
				cancelled.Store(true)
			}
			return err
		}
	}
	job, err := procgroup.NewJob(stepJobName())
	if err != nil {
		slog.Default().Debug("step job unavailable; running unowned", "command", display, "err", err)
		trackCancellation(cmd.Cancel)
		return stepJob{cancelled: cancelled, cmd: cmd}, cmd.Start()
	}
	started := make(chan struct{})
	trackCancellation(func() error {
		// safety: cancellation cannot target an empty job while its leader is still being assigned.
		<-started
		process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
		if err != nil {
			_ = job.Terminate()
			if err == windows.ERROR_INVALID_PARAMETER {
				return os.ErrProcessDone
			}
			return err
		}
		defer windows.CloseHandle(process)
		state, err := windows.WaitForSingleObject(process, 0)
		if err != nil {
			_ = job.Terminate()
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			_ = job.Terminate()
			return os.ErrProcessDone
		}
		return finishWindowsStepCancellation(process, job)
	})
	startErr := job.Start(cmd)
	close(started)
	if startErr != nil {
		_ = job.Close()
		if cmd.Process != nil {
			_ = cmd.Wait()
		}
		return stepJob{}, startErr
	}
	return stepJob{job: job, cancelled: cancelled, cmd: cmd}, nil
}

func finishWindowsStepCancellation(process windows.Handle, job *procgroup.Job) error {
	if err := job.TerminateWithCode(stepCancellationExitCode); err != nil {
		return err
	}
	state, err := windows.WaitForSingleObject(process, 5000)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("Windows step termination did not complete: wait status %d", state)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil {
		return err
	}
	if code != stepCancellationExitCode {
		return os.ErrProcessDone
	}
	return nil
}

func (j stepJob) close() {
	if j.job != nil {
		_ = j.job.Close()
	}
}

func ownCommandGroup(*exec.Cmd) func() { return func() {} }

// recordStepSession writes the ledger record a sweep outside this process
// needs to end the job if this process dies first. Kill-on-close already
// ends the job when this process's handle goes away, so the record mostly
// tells doctor what happened; it still matters when the handle outlives
// the node's usefulness.
func recordStepSession(ctx context.Context, cmd *exec.Cmd, display string, job stepJob) func() {
	run := os.Getenv("SPARKWING_RUN_ID")
	node := NodeFromContext(ctx)
	if node == "" {
		node = os.Getenv("SPARKWING_NODE_ID")
	}
	if run == "" || node == "" || job.job == nil || cmd.Process == nil {
		return func() {}
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return func() {}
	}
	ownerBirth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		return func() {}
	}
	release, err := sessionledger.Open(p.SessionLedgerDir()).Record(sessionledger.Record{
		Run: run, Node: node, OwnerPID: os.Getpid(), OwnerBirth: ownerBirth,
		Handle:  sessionledger.Handle{Kind: "job", LeaderPID: cmd.Process.Pid, JobName: job.job.Name},
		Command: display,
	})
	if err != nil {
		slog.Default().Debug("step session not recorded", "err", err)
		return func() {}
	}
	return release
}
