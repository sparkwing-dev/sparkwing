package bincache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// fetchCredentialAsks is how many times a pipe answers git's credential
// helper: one fetch asks once or twice, and a submodule update asks once
// per submodule it fetches.
const fetchCredentialAsks = 32

func hasGitmodules(checkout string) bool {
	fi, err := os.Lstat(filepath.Join(checkout, ".gitmodules"))
	return err == nil && fi.Mode().IsRegular()
}

// directSubmodules checks out the submodules of checkout with cred, which
// the controller released for the run. An ssh submodule URL on the
// credential's host is rewritten to https for a token, and an https one to
// ssh for a deploy key, so the one credential serves them all. Like the
// fetch, it reads none of the machine's git config and runs no hook.
func directSubmodules(ctx context.Context, checkout, scope string, cred DirectCredential, opts directOptions) (err error) {
	defer func() { err = redactCredential(err, cred) }()
	if cred.Empty() {
		return errors.New("direct source: submodules are fetched only with a credential the controller released")
	}
	base := append(directGitEnv(os.Environ()), "GIT_ALLOW_PROTOCOL="+opts.protocols, "GIT_TERMINAL_PROMPT=0")
	env := credentialFetchEnv(directLocalGitEnv(base))
	var extra []*os.File
	switch cred.Kind {
	case CredentialGitHubApp, CredentialHTTPS:
		prefix := strings.TrimRight(scope, "/") + "/"
		env = withPipeCredential(env, scope)
		env = withGitConfig(env,
			"url."+prefix+".insteadOf", "git@"+cred.Host+":",
			"url."+prefix+".insteadOf", "ssh://git@"+cred.Host+"/")
		pipe, err := credentialPipe(cred.Username, cred.Secret, fetchCredentialAsks)
		if err != nil {
			return fmt.Errorf("direct source: submodule credential pipe: %w", err)
		}
		defer func() { _ = pipe.Close() }()
		extra = []*os.File{pipe}
		env = append(env, "GIT_SSH_COMMAND=ssh"+directSSHOptions)
	case CredentialSSH:
		_, command, cleanup, err := writeSSHCredential(cred, opts.keyTmpfsOnly)
		if err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
		defer func() {
			if rmErr := cleanup(); rmErr != nil {
				err = errors.Join(err, fmt.Errorf("direct source: remove the deploy key: %w", rmErr))
			}
		}()
		env = withGitConfig(env, "url.ssh://git@"+cred.Host+"/.insteadOf", "https://"+cred.Host+"/")
		env = append(env, "GIT_SSH_COMMAND="+command)
	default:
		return fmt.Errorf("direct source: unknown credential kind %q", cred.Kind)
	}
	if opts.fetchTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.fetchTimeout)
		defer cancel()
	}
	// safety: jobs 1 keeps the helper's asks in order on the one pipe.
	cmd := exec.CommandContext(ctx, "git", "-C", checkout, "-c", "core.hooksPath=/dev/null",
		"-c", "http.followRedirects=false", "-c", "protocol.file.allow=never",
		"submodule", "update", "--init", "--recursive", "--depth", "1", "--jobs", "1")
	cmd.Env = env
	cmd.ExtraFiles = extra
	killGroupOnCancel(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("direct source: check out submodules: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
