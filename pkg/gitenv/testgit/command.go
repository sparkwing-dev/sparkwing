// Package testgit runs fixture commands without inherited Git repository bindings.
package testgit

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// Env removes ambient Git controls while retaining config selection and prompt policy.
// Add any repository binding a test needs explicitly afterward.
func Env(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if keep(name) {
			result = append(result, entry)
		}
	}
	return result
}

func keep(name string) bool {
	switch strings.ToUpper(name) {
	case "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT":
		return true
	default:
		return !strings.HasPrefix(strings.ToUpper(name), "GIT_")
	}
}

// Unbind scrubs a fresh test suite before production code can inherit Git controls.
// Re-executed fixture processes can still receive explicit bindings from their tests.
func Unbind() error {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !keep(name) {
			if err := os.Unsetenv(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// Command is exec.Command with Git controls removed from the inherited environment.
// It also accepts a resolved Git executable or a script that runs Git.
func Command(name string, args ...string) *exec.Cmd {
	return procenv.ControlCommand(Env(os.Environ()), name, args...)
}

// CommandContext is Command with cancellation tied to ctx.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return procenv.ControlCommandContext(ctx, Env(os.Environ()), name, args...)
}
