package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/source/gitcmd"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

type gitClient struct {
	env             []string
	credential      Credential
	workspace       string
	bundleTransport http.RoundTripper
}

func gitEnvironment(base []string) []string {
	return append(append([]string{}, base...), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:ssh", "GIT_LFS_SKIP_SMUDGE=1")
}

func (g *gitClient) run(ctx context.Context, directory, remote string, args ...string) (string, error) {
	out, err := g.output(ctx, directory, remote, "", args...)
	return strings.TrimSpace(string(out)), err
}

func (g *gitClient) output(ctx context.Context, directory, remote, input string, args ...string) (_ []byte, err error) {
	commandArgs := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "http.followRedirects=false", "-c", "credential.helper=", "-c", "core.useReplaceRefs=false"}
	env := append([]string{}, g.env...)
	if remote != "" {
		pin, err := sourceurl.PinCloneHost(ctx, remote, net.DefaultResolver.LookupIPAddr)
		if err != nil {
			return nil, err
		}
		config, fetchEnv, cleanup, err := g.authentication(ctx, remote)
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, cleanup()) }()
		commandArgs = append(commandArgs, config...)
		env = append(env, pinnedEnvironment(pin, fetchEnv)...)
		if !pin.SSH {
			commandArgs = append(commandArgs, "-c", "http.curloptResolve="+pin.Host+":"+pin.Port+":"+curlAddresses(pin.Addrs))
		}
	}
	if directory != "" {
		commandArgs = append(commandArgs, "-C", directory)
	}
	cmd := exec.CommandContext(ctx, "git", append(commandArgs, args...)...)
	cmd.Env = env
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := gitcmd.Output(ctx, cmd, false)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// safety: remote failures can echo credentials, signed URLs and helper output.
		return nil, fmt.Errorf("source git %s failed: %w", args[0], err)
	}
	return out, nil
}

func pinnedEnvironment(pin sourceurl.Pin, env []string) []string {
	if !pin.SSH {
		return env
	}
	out := append([]string{}, env...)
	for i, item := range out {
		if command, ok := strings.CutPrefix(item, "GIT_SSH_COMMAND="); ok {
			out[i] = "GIT_SSH_COMMAND=" + command + " -o CheckHostIP=no -o HostName=" + pin.Addrs[0] + " -o HostKeyAlias=" + pin.Host
		}
	}
	return out
}

func curlAddresses(addrs []string) string {
	out := make([]string, len(addrs))
	for i, addr := range addrs {
		out[i] = addr
		if strings.Contains(addr, ":") {
			out[i] = "[" + addr + "]"
		}
	}
	return strings.Join(out, ",")
}

func gitExit(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}
