package bincache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// RunSource is one run's source as a direct runner fetches it.
type RunSource struct {
	ControllerURL string
	RunnerToken   string
	RunID         string
	RepoURL       string
	Branch        string
	SHA           string
	WorkDir       string
	// OwnerCredentials marks a runner its owner fenced with --allow-repo: it
	// may fetch with the machine's own git credentials when the controller
	// releases none. Every other runner fetches only with what the
	// controller releases for the run.
	OwnerCredentials bool
}

// FetchRunSourceDirect resolves the run's source credential and checks out
// its source. The controller decides the credential: the team's GitHub App
// token when an installation covers the repository, else the git credential
// the team stored for the repository's host. When it releases none, a runner
// without OwnerCredentials fails with the controller's remedy, and a runner
// with them fetches with the machine's own credentials.
func FetchRunSourceDirect(ctx context.Context, src RunSource, logger *slog.Logger) (string, error) {
	return fetchRunSourceDirect(ctx, src, logger, func(cred DirectCredential) (string, error) {
		return FetchPipelineSourceDirect(ctx, src.RepoURL, src.Branch, src.SHA, src.WorkDir, cred)
	})
}

func fetchRunSourceDirect(ctx context.Context, src RunSource, logger *slog.Logger,
	fetch func(DirectCredential) (string, error),
) (string, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cred, err := RequestDirectCredential(ctx, src.ControllerURL, src.RunnerToken, src.RunID, nil)
	if err != nil {
		// safety: a runner its owner did not fence has no credentials of its
		// own to offer, so it fails rather than fetch as whoever runs it.
		if !src.OwnerCredentials {
			return "", fmt.Errorf("direct source: %w", err)
		}
		logger.Info("direct source: the controller released no credential; fetching with this machine's own",
			"run_id", src.RunID, "reason", err.Error())
		cred = DirectCredential{}
	}
	return fetch(cred)
}

// credentialRemote fits remote to the transport cred authenticates: the https
// form for a token, the ssh form for a deploy key. It refuses a remote on any
// host but the one the credential is bound to.
func credentialRemote(remote string, cred DirectCredential) (string, error) {
	if cred.Empty() {
		return remote, nil
	}
	host, err := sourceurl.Host(remote)
	if err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	if host != cred.Host {
		return "", fmt.Errorf("direct source: the %s credential is bound to %s, and the run fetches from %s",
			cred.Kind, cred.Host, host)
	}
	var shaped string
	switch cred.Kind {
	case CredentialGitHubApp:
		shaped = githubTokenRemote(remote)
		if shaped == "" {
			return "", errors.New("direct source: a GitHub source token fetches only a github.com repository")
		}
	case CredentialHTTPS:
		shaped, err = sourceurl.HTTPSForm(remote)
	case CredentialSSH:
		shaped, err = sourceurl.SSHForm(remote)
	default:
		return "", fmt.Errorf("direct source: unknown credential kind %q", cred.Kind)
	}
	if err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	return shaped, nil
}

func githubTokenRemote(remote string) string {
	if https := githubHTTPS(remote); https != "" {
		return https
	}
	if strings.HasPrefix(strings.ToLower(remote), "https://github.com/") {
		return remote
	}
	return ""
}

// credentialScope is the URL prefix git hands a pipe credential to: the
// remote's own scheme and host, so no other server is ever answered.
func credentialScope(remote string) (string, error) {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("no credential scope for %s", sourceurl.Redact(remote))
	}
	return u.Scheme + "://" + u.Host + "/", nil
}

// credentialFetchEnv is the environment of a fetch that presents a released
// credential: localEnv, which reads no system, global or environment git
// config, less every ssh agent, askpass and ssh program the machine names.
// Nothing of the machine's own can then answer for the fetch.
func credentialFetchEnv(localEnv []string) []string {
	out := make([]string, 0, len(localEnv))
	for _, item := range localEnv {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "SSH_AUTH_SOCK", "SSH_AGENT_PID", "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "GIT_ASKPASS",
			"GIT_SSH", "GIT_SSH_COMMAND", "GIT_SSH_VARIANT", "DISPLAY":
			continue
		}
		out = append(out, item)
	}
	return out
}

// directSSHKeyOptions pin an ssh fetch to the released deploy key and the
// host key its owner confirmed: no other identity, agent, config file or
// known_hosts entry takes part.
const directSSHKeyOptions = " -F /dev/null -o IdentitiesOnly=yes -o IdentityAgent=none" +
	" -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UpdateHostKeys=no"

// writeSSHCredential writes cred's key and pinned host key into a fresh
// private directory, on tmpfs where the machine has one, and returns the
// directory, which the caller removes, and the GIT_SSH_COMMAND that uses them.
func writeSSHCredential(cred DirectCredential) (dir, command string, err error) {
	dir, err = os.MkdirTemp(sshKeyRoot(), "sparkwing-git-")
	if err != nil {
		return "", "", fmt.Errorf("ssh credential directory: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	key := strings.TrimRight(cred.Secret, "\n") + "\n"
	knownHosts := strings.TrimRight(cred.KnownHosts, "\n") + "\n"
	keyPath, hostsPath := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	for path, body := range map[string]string{keyPath: key, hostsPath: knownHosts} {
		if err := writePrivateFile(path, body); err != nil {
			return "", "", fmt.Errorf("ssh credential: %w", err)
		}
	}
	command = "ssh -i " + shellQuote(keyPath) + directSSHKeyOptions +
		" -o UserKnownHostsFile=" + shellQuote(hostsPath) + directSSHOptions
	return dir, command, nil
}

func writePrivateFile(path, body string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(body)
	return errors.Join(werr, f.Close())
}

// sshKeyRoot is where a released deploy key is written: /dev/shm, a tmpfs,
// where the machine has one, so the key never reaches a disk.
func sshKeyRoot() string {
	if runtime.GOOS == "linux" {
		if fi, err := os.Stat("/dev/shm"); err == nil && fi.IsDir() {
			return "/dev/shm"
		}
	}
	return os.TempDir()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// redactCredential replaces every trace of cred's secret in err's text: the
// whole value and, for a key, each of its lines. It is the backstop for a
// transport that echoes what it was handed into the output an error carries
// to logs and to the run's failure message.
func redactCredential(err error, cred DirectCredential) error {
	if err == nil || cred.Secret == "" {
		return err
	}
	msg := err.Error()
	redacted := msg
	for _, v := range credentialFragments(cred.Secret) {
		redacted = strings.ReplaceAll(redacted, v, "***")
	}
	if redacted == msg {
		return err
	}
	// safety: the cause is dropped rather than wrapped, since anything that
	// unwraps and prints it would print the secret.
	return errors.New(redacted)
}

// credentialFragments are the pieces of secret a log could carry apart from
// the whole: each line of a multi-line key long enough to be key material.
func credentialFragments(secret string) []string {
	out := []string{secret}
	for _, line := range strings.Split(secret, "\n") {
		line = strings.TrimSpace(line)
		if len(line) >= 16 && !strings.HasPrefix(line, "-----") {
			out = append(out, line)
		}
	}
	return out
}
