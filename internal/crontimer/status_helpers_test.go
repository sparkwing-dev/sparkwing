package crontimer

import (
	"context"
)

// Status reports the timer without changing anything. A service manager that
// cannot be reached -- no user session under a bare WSL, no launchd domain --
// leaves Enabled false and says why in Detail; it is not an error, because the
// files on disk still answer the question that was asked.
func Status(ctx context.Context, h Host) (State, error) {
	if err := checkOperation(ctx, h); err != nil {
		return State{}, err
	}
	switch h.GOOS {
	case "linux":
		return statusLinux(ctx, h)
	case "darwin":
		return statusDarwin(ctx, h)
	}
	return State{}, unsupported(h.GOOS)
}
