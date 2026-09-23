package bincache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

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
//
// When a team owner listed extra repositories for the run's repository and
// the checkout has submodules, the runner checks them out with the same
// credential, which the controller widened to the listed repositories when it
// is an App token. Nothing in the fetched tree widens it.
func FetchRunSourceDirect(ctx context.Context, src RunSource, logger *slog.Logger) (string, error) {
	opts := defaultDirectOptions()
	// safety: a runner its owner did not fence is a cloud runner, whose
	// released keys only ever touch memory.
	opts.keyTmpfsOnly = !src.OwnerCredentials
	return fetchRunSourceDirect(ctx, src, logger, func(cred DirectCredential) (string, error) {
		fetchOpts := opts
		fetchOpts.cred = cred
		return fetchPipelineSourceDirect(ctx, src.RepoURL, src.Branch, src.SHA, src.WorkDir, fetchOpts)
	}, func(checkout string, cred DirectCredential) error {
		return directSubmodules(ctx, checkout, "https://"+cred.Host+"/", cred, opts)
	})
}

func fetchRunSourceDirect(ctx context.Context, src RunSource, logger *slog.Logger,
	fetch func(DirectCredential) (string, error), submodules func(string, DirectCredential) error,
) (string, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	cred, err := RequestDirectCredential(ctx, src.ControllerURL, src.RunnerToken, src.RunID)
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
	sparkwingDir, err := fetch(cred)
	if err != nil || cred.Empty() || len(cred.ExtraRepositories) == 0 {
		return sparkwingDir, err
	}
	checkout := filepath.Dir(sparkwingDir)
	if !hasGitmodules(checkout) {
		return sparkwingDir, nil
	}
	if err := submodules(checkout, cred); err != nil {
		return "", err
	}
	return sparkwingDir, nil
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
// private directory and returns it, the GIT_SSH_COMMAND that uses them, and
// the cleanup that removes the directory. The directory is on a tmpfs where
// the machine has one; with tmpfsOnly, which a cloud runner sets, it is
// refused rather than written to a disk. A lock held on the directory until
// the cleanup tells a starting runner's sweep that the key is still in use.
func writeSSHCredential(cred DirectCredential, tmpfsOnly bool) (dir, command string, cleanup func() error, err error) {
	root, err := sshKeyRoot(tmpfsOnly)
	if err != nil {
		return "", "", nil, err
	}
	dir, err = os.MkdirTemp(root, keyDirPrefix)
	if err != nil {
		return "", "", nil, fmt.Errorf("ssh credential directory: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, keyDirLockName), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		if _, lerr := cacheLock(lock, cacheLockExclusive); lerr != nil {
			err = errors.Join(lerr, lock.Close())
		}
	}
	if err != nil {
		return "", "", nil, errors.Join(fmt.Errorf("ssh credential lock: %w", err), os.RemoveAll(dir))
	}
	cleanup = func() error {
		return errors.Join(os.RemoveAll(dir), lock.Close())
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
			cleanup = nil
		}
	}()
	key := strings.TrimRight(cred.Secret, "\n") + "\n"
	knownHosts := strings.TrimRight(cred.KnownHosts, "\n") + "\n"
	keyPath, hostsPath := filepath.Join(dir, "key"), filepath.Join(dir, "known_hosts")
	for path, body := range map[string]string{keyPath: key, hostsPath: knownHosts} {
		if err := writePrivateFile(path, body); err != nil {
			return "", "", nil, fmt.Errorf("ssh credential: %w", err)
		}
	}
	command = "ssh -i " + shellQuote(keyPath) + directSSHKeyOptions +
		" -o UserKnownHostsFile=" + shellQuote(hostsPath) + directSSHOptions
	return dir, command, cleanup, nil
}

func writePrivateFile(path, body string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(body)
	return errors.Join(werr, f.Close())
}

const (
	keyDirPrefix   = "sparkwing-git-"
	keyDirLockName = "lock"
)

// keyRootCandidates are where a released deploy key may be written, in
// order of preference; onTmpfs reports which are memory-backed.
var (
	keyRootCandidates = func() []string {
		roots := []string{"/dev/shm"}
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			roots = append(roots, dir)
		}
		return append(roots, os.TempDir())
	}
	onTmpfs = isTmpfs
)

// sshKeyRoot is the first key root on a tmpfs, so the key never reaches a
// disk. Without one, tmpfsOnly refuses, and otherwise the key goes to the
// temporary directory.
func sshKeyRoot(tmpfsOnly bool) (string, error) {
	for _, dir := range keyRootCandidates() {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() && onTmpfs(dir) {
			return dir, nil
		}
	}
	if tmpfsOnly {
		return "", errors.New("no tmpfs to hold the released deploy key: a cloud runner never writes one to disk; " +
			"give the runner a tmpfs at /dev/shm")
	}
	return os.TempDir(), nil
}

// SweepSSHKeyDirs removes the deploy key directories a runner of this user
// left behind when it crashed mid-fetch, from every key root, and reports how
// many it removed. It leaves a directory whose lock a live fetch holds, one
// of another user, and anything that is not a directory, such as a symlink.
func SweepSSHKeyDirs() (int, error) {
	removed := 0
	var errs []error
	seen := map[string]bool{}
	for _, root := range keyRootCandidates() {
		if seen[root] {
			continue
		}
		seen[root] = true
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), keyDirPrefix) {
				continue
			}
			dir := filepath.Join(root, e.Name())
			fi, err := os.Lstat(dir)
			if err != nil || !fi.IsDir() || !ownedByThisUser(fi) || keyDirInUse(dir, fi) {
				continue
			}
			if err := os.RemoveAll(dir); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// keyDirInUse reports whether a live fetch holds dir's lock. A directory
// with no lock file is in use only in the moment between its creation and
// the lock's, so one older than a minute is a leftover.
func keyDirInUse(dir string, fi os.FileInfo) bool {
	lock, err := os.Open(filepath.Join(dir, keyDirLockName))
	if err != nil {
		return !os.IsNotExist(err) || time.Since(fi.ModTime()) < time.Minute
	}
	defer func() { _ = lock.Close() }()
	got, err := cacheLock(lock, cacheLockExclusiveNonblock)
	return err != nil || !got
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
