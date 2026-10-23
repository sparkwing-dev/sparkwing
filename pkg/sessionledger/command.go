package sessionledger

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// runCleanupCommand runs a library-registered cleanup argv once, directly
// (never through a shell), when the owner that registered it is gone. The
// command should be idempotent: the reaper attempts it a single time and
// drops the record whatever the exit, because it runs in a sweeping process
// (a later run, the daemon, or doctor) whose environment may differ from the
// step's, and retrying a command it cannot judge would only accumulate noise.
//
// safety: the error names only the program, since a sweep copies it into a
// stray_session_reaped event and its path, arguments and output can quote a
// secret no sweeping process can mask.
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
	return fmt.Errorf("cleanup %s: %w", filepath.Base(argv[0]), err)
}
