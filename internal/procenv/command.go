package procenv

import (
	"context"
	"os/exec"
)

// Command constructs a process with a policy-owned environment. The zero Env
// means an empty environment, never inheritance.
func Command(environment Env, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append([]string{}, environment.values...)
	return cmd
}

// CommandContext is Command with cancellation bound to ctx.
func CommandContext(ctx context.Context, environment Env, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{}, environment.values...)
	return cmd
}

// ControlCommand constructs trusted host tooling with an explicit captured environment.
// Repository-controlled processes use Command with Build's result.
func ControlCommand(environment []string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = append([]string{}, environment...)
	return cmd
}

// ControlCommandContext is ControlCommand with cancellation bound to ctx.
func ControlCommandContext(ctx context.Context, environment []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append([]string{}, environment...)
	return cmd
}

// ConfigureWindowsBash carries a Bash program beyond Windows' command-line limit.
func ConfigureWindowsBash(cmd *exec.Cmd, program string) {
	cmd.Env = append(cmd.Environ(), "SPARKWING_BASH_PROGRAM="+program)
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
