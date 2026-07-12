//go:build !unix

package sparkwing

import (
	"os/exec"
	"time"
)

// configureProcessGroup is a no-op off unix; exec.CommandContext's default
// cancellation (kill the direct child) stands.
func configureProcessGroup(cmd *exec.Cmd) {}

// commandResourceUsage reports not-measured off unix, where no wait4 rusage
// is available; subprocess cost is simply not folded in rather than reported
// as a wrong zero.
func commandResourceUsage(cmd *exec.Cmd) (time.Duration, int64, bool) {
	return 0, 0, false
}
