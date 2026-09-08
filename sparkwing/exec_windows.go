//go:build windows

package sparkwing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func configureProcessGroup(context.Context, *exec.Cmd, <-chan struct{}) {}

func commandResourceUsage(*exec.Cmd) (time.Duration, int64, bool) { return 0, 0, false }

// stepJob is the kill-on-close Job Object one step command runs inside. It
// is what a timeout terminates and what a sweep can open by name once this
// process is gone.
type stepJob struct{ job *procgroup.Job }

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
	job, err := procgroup.NewJob(stepJobName())
	if err != nil {
		slog.Default().Debug("step job unavailable; running unowned", "command", display, "err", err)
		return stepJob{}, cmd.Start()
	}
	cmd.Cancel = job.Terminate
	if err := job.Start(cmd); err != nil {
		_ = job.Close()
		return stepJob{}, err
	}
	return stepJob{job: job}, nil
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
