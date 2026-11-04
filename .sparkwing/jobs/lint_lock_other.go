//go:build !unix

package jobs

import "context"

// hack: no flock outside unix, so the gate keeps golangci-lint's own serial
// wait, and that wait counts against the lint deadline there.
func runGolangciLint(ctx context.Context) error {
	return runGolangciLintOver(ctx, nil)
}

func runGolangciLintOver(ctx context.Context, targets map[string][]string) error {
	return lintModules(ctx, lintLockWait, lintCommand, false, targets)
}
