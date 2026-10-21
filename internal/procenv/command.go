package procenv

import (
	"context"
	"os"
	"os/exec"
)

// Command constructs a process with an explicit environment. Trusted control
// processes pass their captured host environment; pipeline callers pass Build's
// boundary-specific result. Nil means an empty environment, never inheritance.
func Command(environment []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append([]string{}, environment...)
	return cmd
}

// CommandContext is Command with cancellation bound to ctx.
func CommandContext(ctx context.Context, environment []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{}, environment...)
	return cmd
}

// AppendEnvironment appends platform execution settings to an explicitly
// configured command without exposing its environment backing array.
func AppendEnvironment(cmd *exec.Cmd, values ...string) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, values...)
}

// HostCommand constructs a trusted control process with the host environment.
// It preserves os/exec inheritance, including PWD after the caller selects Dir.
// Pipeline processes use Command with their boundary-specific environment.
func HostCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// HostCommandContext is HostCommand with cancellation bound to ctx.
func HostCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
