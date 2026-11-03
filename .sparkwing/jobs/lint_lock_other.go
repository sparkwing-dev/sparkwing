//go:build !unix

package jobs

import "context"

// hack: no flock outside unix, so the gate keeps golangci-lint's own serial
// wait, and that wait counts against the lint deadline there.
func runGolangciLint(ctx context.Context) error {
	return runGolangciLintWithin(ctx, lintLockWait)
}
