package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

type gitClient struct {
	env        []string
	credential Credential
	workspace  string
}

func gitEnvironment(base []string) []string {
	var env []string
	for _, assignment := range base {
		name, _, _ := strings.Cut(assignment, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			env = append(env, assignment)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh", "GIT_LFS_SKIP_SMUDGE=1")
}

func (g *gitClient) run(ctx context.Context, directory, remote string, args ...string) (output string, err error) {
	commandArgs := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "http.followRedirects=false", "-c", "credential.helper="}
	env := append([]string{}, g.env...)
	if remote != "" {
		if err := sourceurl.CheckResolvedHost(ctx, remote, net.DefaultResolver.LookupIPAddr); err != nil {
			return "", err
		}
		config, fetchEnv, cleanup, err := g.authentication(ctx, remote)
		if err != nil {
			return "", err
		}
		defer func() { err = errors.Join(err, cleanup()) }()
		commandArgs = append(commandArgs, config...)
		env = append(env, fetchEnv...)
	}
	if directory != "" {
		commandArgs = append(commandArgs, "-C", directory)
	}
	cmd := exec.CommandContext(ctx, "git", append(commandArgs, args...)...)
	cmd.Env = env
	out, err := commandOutput(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// safety: remote failures can echo credentials, signed URLs and helper output.
		return "", fmt.Errorf("source git %s failed: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitExit(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}
