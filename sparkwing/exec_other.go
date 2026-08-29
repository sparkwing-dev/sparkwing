//go:build !unix

package sparkwing

import (
	"context"
	"os/exec"
	"time"
)

func configureProcessGroup(context.Context, *exec.Cmd) {}

func commandResourceUsage(cmd *exec.Cmd) (time.Duration, int64, bool) {
	return 0, 0, false
}
