// Package testgit runs fixture commands without inherited Git repository bindings.
package testgit

import (
	"context"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/gitcontrol"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// Command is exec.Command with Git controls removed from the inherited environment.
// It also accepts a resolved Git executable or a script that runs Git.
func Command(name string, args ...string) *exec.Cmd {
	return procenv.ControlCommand(gitcontrol.Env(os.Environ()), name, args...)
}

// CommandContext is Command with cancellation tied to ctx.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return procenv.ControlCommandContext(ctx, gitcontrol.Env(os.Environ()), name, args...)
}
