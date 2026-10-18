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
	owner           bool
	combined        bool
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
		// safety: only the owning user's own clone may reach a host by ssh alias;
		// a tenant clone connects to the address that passed the check.
		var pin sourceurl.Pin
		if g.owner {
			err = sourceurl.CheckResolvedHost(ctx, remote, lookupHost)
		} else {
			remote, args, err = normalizedRemote(remote, args)
			if err == nil {
				pin, err = sourceurl.PinCloneHost(ctx, remote, lookupHost)
			}
		}
		if err != nil {
			return nil, err
		}
		config, fetchEnv, cleanup, err := g.authentication(ctx, remote)
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, cleanup()) }()
		commandArgs = append(commandArgs, config...)
		if g.owner {
			env = append(env, fetchEnv...)
		} else {
			env = append(env, pinnedEnvironment(pin, fetchEnv)...)
		}
		if !g.owner && !pin.SSH {
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
	out, err := gitcmd.Output(ctx, cmd, g.combined)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// safety: remote failures can echo credentials, signed URLs and helper
		// output, so only a combined local read returns its output on failure.
		if !g.combined {
			out = nil
		}
		return out, fmt.Errorf("source git %s failed: %w", args[0], err)
	}
	return out, nil
}

func tenantRemote(remote string, options Options) (string, error) {
	if remote == "" || options.Base.Local {
		return remote, nil
	}
	return sourceurl.NormalizeCloneHost(remote)
}

var lookupHost sourceurl.Lookup = net.DefaultResolver.LookupIPAddr

// safety: curl and ssh match the pin by the spelling git dials, so a trailing
// dot or a capital letter would dial a fresh resolution.
func normalizedRemote(remote string, args []string) (string, []string, error) {
	normalized, err := sourceurl.NormalizeCloneHost(remote)
	if err != nil {
		return "", nil, err
	}
	out := append([]string{}, args...)
	for i, arg := range out {
		if arg == remote {
			out[i] = normalized
		}
	}
	return normalized, out, nil
}

func pinnedEnvironment(pin sourceurl.Pin, env []string) []string {
	if !pin.SSH {
		return env
	}
	out := append([]string{}, env...)
	for i, item := range out {
		if command, ok := strings.CutPrefix(item, "GIT_SSH_COMMAND="); ok {
			out[i] = "GIT_SSH_COMMAND=" + command + " -o CheckHostIP=no -o HostName=" + shellQuote(pin.Addrs[0]) + " -o HostKeyAlias=" + shellQuote(pin.Host)
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
