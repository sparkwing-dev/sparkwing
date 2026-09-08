package sessionledger

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// runCleanupCommand runs a library-registered cleanup argv once, directly
// (never through a shell), when the owner that registered it is gone. The
// command should be idempotent: the reaper attempts it a single time and
// drops the record whatever the exit, because it runs in a sweeping process
// (a later run, the daemon, or doctor) whose environment may differ from the
// step's, and retrying a command it cannot judge would only accumulate noise.
func runCleanupCommand(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return errors.New("sessionledger: command handle carries no argv")
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("cleanup %s: %w: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
