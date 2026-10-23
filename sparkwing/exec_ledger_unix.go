//go:build unix

package sparkwing

import (
	"context"
	"log/slog"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/pkg/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/procgroup"
	"github.com/sparkwing-dev/sparkwing/pkg/sessionledger"
)

// recordStepSession writes the ledger record a sweep outside this process
// needs to end the command's session if this process dies first. Outside a
// run there is no owner to sweep for, so nothing is written.
func recordStepSession(ctx context.Context, cmd *exec.Cmd, display string, _ stepJob) func() {
	run := os.Getenv("SPARKWING_RUN_ID")
	node := nodeFromContext(ctx)
	if node == "" {
		node = os.Getenv("SPARKWING_JOB_ID")
	}
	if run == "" || node == "" || cmd.Process == nil {
		return func() {}
	}
	p, err := paths.DefaultPaths()
	if err != nil {
		return func() {}
	}
	leader, err := procgroup.CaptureSession(cmd.Process.Pid)
	if err != nil {
		slog.Default().Debug("step session not recorded", "err", err)
		return func() {}
	}
	ownerBirth, err := procgroup.ProcessBirth(os.Getpid())
	if err != nil {
		slog.Default().Debug("step session not recorded", "err", err)
		return func() {}
	}
	release, err := sessionledger.Open(p.SessionLedgerDir()).Record(sessionledger.Record{
		Run:        run,
		Node:       node,
		OwnerPID:   os.Getpid(),
		OwnerBirth: ownerBirth,
		Handle: sessionledger.Handle{
			Kind:        "session",
			LeaderPID:   leader.LeaderPID,
			SessionID:   leader.SessionID,
			LeaderBirth: leader.BirthToken,
		},
		Command: sessionledger.MaskCommand(ctx, display),
	})
	if err != nil {
		slog.Default().Debug("step session not recorded", "err", err)
		return func() {}
	}
	return release
}
