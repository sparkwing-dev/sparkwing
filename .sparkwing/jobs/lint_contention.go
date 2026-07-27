package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// lintLockWait bounds how long the lint step waits for golangci-lint's
// box-wide parallel-runner lock. --allow-serial-runners on its own
// waits forever, so without a bound one wedged linter anywhere on the
// machine would pin every gate that queues behind it.
const lintLockWait = 5 * time.Minute

// golangciContention is what golangci-lint prints when it gives up on
// the lock. Worth matching even with --allow-serial-runners passed: an
// older binary that predates the flag, or a config that re-enables the
// default, still produces it, and it must not read as a finding.
const golangciContention = "parallel golangci-lint is running"

// lintCommand is the only golangci-lint command line this repo's gates
// run. --allow-serial-runners is what makes a run that cannot take the
// box-wide lock wait for it; without the flag the run retries for 5s
// and exits, and the gate charges a neighbouring build to the change
// under test.
const lintCommand = "cd .sparkwing && golangci-lint run --allow-serial-runners ./..."

// runGolangciLint lints the .sparkwing module, waiting out any other
// golangci-lint on the box for up to lintLockWait. The returned error
// already reads as a gate failure line, so a caller collecting failures
// can take it unchanged.
func runGolangciLint(ctx context.Context) error {
	lintCtx, cancel := context.WithTimeout(ctx, lintLockWait)
	defer cancel()

	start := time.Now()
	_, err := sparkwing.Bash(lintCtx, lintCommand).
		Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
		Run()
	if err == nil {
		return nil
	}
	return errors.New(describeLintFailure(lintCtx, time.Since(start), err))
}

// describeLintFailure says what is known about a failed lint step. The
// bare exec error is "command failed (exit 1)", which reads as a lint
// finding and sends the reader hunting for a regression in a tree that
// is clean. Contention is named only where golangci-lint itself
// reported it; a wait that runs out establishes that the step did not
// finish, never why, so that case reports the time actually waited and
// leaves the cause as the likelihood it is.
func describeLintFailure(ctx context.Context, waited time.Duration, err error) string {
	var execErr *sparkwing.ExecError
	if errors.As(err, &execErr) && strings.Contains(execErr.Stdout+execErr.Stderr, golangciContention) {
		return "golangci-lint: could not run -- another golangci-lint holds the box-wide " +
			"lock. That is contention, not a finding in this tree."
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("golangci-lint: could not run -- no result after %s. Most often "+
			"that is the box-wide golangci-lint lock held by another run; either way "+
			"nothing was learned about this tree.", waited.Round(time.Second))
	}
	return fmt.Sprintf("golangci-lint: %v", err)
}
