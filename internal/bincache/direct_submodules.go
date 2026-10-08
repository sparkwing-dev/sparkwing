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

// safety: Each submodule fetch may ask the inherited credential pipe again.
const fetchCredentialAsks = 32

func hasGitmodules(checkout string) bool {
	fi, err := os.Lstat(filepath.Join(checkout, ".gitmodules"))
	return err == nil && fi.Mode().IsRegular()
}

// safety: Submodules use only the released host credential, without host git config or hooks.
func directSubmodules(ctx context.Context, checkout, scope string, cred DirectCredential, opts directOptions, recursive bool) (err error) {
	defer func() { err = redactCredential(err, cred) }()
	if cred.Empty() {
		return errors.New("direct source: submodules are fetched only with a credential the controller released")
	}
	base := append(directGitEnv(os.Environ()), "GIT_ALLOW_PROTOCOL="+opts.protocols, "GIT_TERMINAL_PROMPT=0")
	env := credentialFetchEnv(directLocalGitEnv(base))
	var extra []*os.File
	cleanupCredential := func() error { return nil }
	switch cred.Kind {
	case CredentialGitHubApp, CredentialHTTPS:
		prefix := strings.TrimRight(scope, "/") + "/"
		env = withPipeCredential(env, scope)
		env = withGitConfig(env,
			"url."+prefix+".insteadOf", "git@"+cred.Host+":",
			"url."+prefix+".insteadOf", "ssh://git@"+cred.Host+"/")
		var err error
		env, extra, cleanupCredential, err = prepareCredentialTransport(env, cred.Username, cred.Secret, fetchCredentialAsks)
		if err != nil {
			return fmt.Errorf("direct source: submodule credential transport: %w", err)
		}
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
	args := []string{
		"-C", checkout, "-c", "core.hooksPath=/dev/null",
		"-c", "http.followRedirects=false", "-c", "protocol.file.allow=never",
		"submodule", "update", "--init", "--depth", "1", "--jobs", "1",
	}
	if recursive {
		args = append(args, "--recursive")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	cmd.ExtraFiles = extra
	killGroupOnCancel(cmd)
	out, runErr := gitCommandCombinedOutput(cmd)
	runErr = errors.Join(runErr, cleanupCredential())
	if runErr != nil {
		return fmt.Errorf("direct source: check out submodules: %w: %s", runErr, strings.TrimSpace(string(out)))
	}
	return nil
}
