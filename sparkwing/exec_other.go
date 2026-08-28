//go:build !unix

package sparkwing

import (
	"os/exec"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {}

func commandResourceUsage(cmd *exec.Cmd) (time.Duration, int64, bool) {
	return 0, 0, false
}
