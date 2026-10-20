package jobs

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// perf: the push tier judges only the depguard rules, which parse imports and
// finish in about two seconds over the whole tree. The F1 forbidigo rule and
// the unexport census load every package with types, so they wait for gate.
func checkGuardsFast(ctx context.Context) error {
	return runGuardcheck(ctx, "-fast")
}

func checkGuards(ctx context.Context) error {
	return runGuardcheck(ctx, "")
}

// safety: the guards judge the whole tree without the origin/main baseline the
// main lint config uses, so a violation cannot pass because it predates main.
func runGuardcheck(ctx context.Context, flags string) error {
	_, err := sparkwing.Bash(ctx, "go run ./internal/guardcheck "+flags+" .").
		Env("GOWORK", "off").
		Env("GOLANGCI_LINT_CACHE", pipelineCacheDir("golangci-lint")).
		Run()
	return err
}
