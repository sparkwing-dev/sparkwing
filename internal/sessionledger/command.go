package sessionledger

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// runCleanupCommand runs a library-registered cleanup argv once, directly
// (never through a shell), when the owner that registered it is gone. The
// command should be idempotent: the reaper attempts it a single time and
// drops the record whatever the exit, because controller doctor may run
// in a different environment from the step and cannot judge a retry.
//
// safety: errors omit executable text and output because the sweep copies
// them into durable events without a host masker.
func runCleanupCommand(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return errors.New("sessionledger: command handle carries no argv")
	}
	err := procenv.HostCommandContext(ctx, argv[0], argv[1:]...).Run()
	var pathErr *fs.PathError
	var execErr *exec.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &execErr):
		err = execErr.Err
	case errors.As(err, &pathErr):
		err = pathErr.Err
	}
	return fmt.Errorf("cleanup: %w", err)
}
