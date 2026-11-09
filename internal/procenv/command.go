package procenv

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// SDKCommand constructs a command an SDK asked its host to run. environment is the one
// the SDK process, started with Build's result, would have given the command, so a
// pipeline's own changes still apply; engine-minted credentials never reach it.
// Name resolves against environment and dir before the final argv is validated.
func SDKCommand(environment []string, dir, name string, args ...string) *exec.Cmd {
	return SDKCommandContext(context.Background(), environment, dir, name, args...)
}

// SDKCommandContext applies SDK child environment policy with context cancellation.
func SDKCommandContext(ctx context.Context, environment []string, dir, name string, args ...string) *exec.Cmd {
	environment = sdkCommandEnvironment(environment)
	path, err := LookupCommand(name, environment, dir)
	if err != nil {
		return &exec.Cmd{Path: name, Args: append([]string{name}, args...), Dir: dir, Env: environment, Err: err}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env, cmd.Dir = environment, dir
	refuseUnsafeBatch(cmd)
	return cmd
}

func refuseUnsafeBatch(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		return
	}
	extension := filepath.Ext(strings.TrimRight(cmd.Path, " ."))
	if !strings.EqualFold(extension, ".cmd") && !strings.EqualFold(extension, ".bat") {
		return
	}
	// safety: cmd.exe reparses batch argv, including environment expansion inside quotes.
	for i, arg := range cmd.Args {
		metacharacters := "&|<>^%\"!()\r\n"
		if i == 0 && strings.ContainsAny(arg, " \t") {
			// safety: Go quotes a spaced program path, keeping install-directory parentheses literal.
			metacharacters = "&|<>^%\"!\r\n"
		}
		if strings.ContainsAny(arg, metacharacters) {
			cmd.Err = fmt.Errorf("windows batch command refused: argv[%d] contains cmd.exe metacharacters; use a non-batch executable, such as node.exe with the tool's JavaScript entrypoint", i)
			return
		}
	}
}

func sdkCommandEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		// perf: every engine credential row shares this prefix, so other names skip the registry scan.
		if strings.HasPrefix(envKey(key), engineCredentialPrefix) {
			if row, ok := Lookup(key); ok && (row.Class == Credential || row.Class == Protocol) && row.Owner == "engine" {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

const engineCredentialPrefix = "SPARKWING_"

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
