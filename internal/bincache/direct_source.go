package bincache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// safety: Git checks these protocols after insteadOf rewriting, blocking ambient redirects to local transports.
const directProtocols = "https:ssh"

var directCommitRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidateDirectSource checks what a direct fetch hands git: an https or ssh
// remote with no credential in it (sourceurl.ValidateCloneURL refuses every
// other scheme, a local path, userinfo on https and a leading dash), and a
// full commit id when one is given.
func ValidateDirectSource(repoURL, sha string) (string, string, error) {
	remote, err := sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("direct source: %w", err)
	}
	if strings.ContainsAny(remote, "?#") {
		return "", "", errors.New("direct source: the remote must not carry a query or fragment")
	}
	if sha != "" {
		sha = strings.ToLower(sha)
		if !directCommitRE.MatchString(sha) {
			return "", "", errors.New("direct source: the commit must be a full 40 or 64 digit hex id")
		}
	}
	return remote, sha, nil
}

// DirectFetchURL is the remote a runner fetches repoURL from. A GitHub ssh
// remote becomes its https form when this process has no ssh identity to
// offer, which is the case on a cloud pod: there the fetch can only succeed
// anonymously, and anonymous ssh to GitHub always fails.
func DirectFetchURL(repoURL string) string {
	if sshIdentityAvailable() {
		return repoURL
	}
	if https := githubHTTPS(repoURL); https != "" {
		return https
	}
	return repoURL
}

func githubHTTPS(repoURL string) string {
	var path string
	switch {
	case strings.HasPrefix(repoURL, "git@github.com:"):
		path = strings.TrimPrefix(repoURL, "git@github.com:")
	case strings.HasPrefix(repoURL, "ssh://git@github.com/"):
		path = strings.TrimPrefix(repoURL, "ssh://git@github.com/")
	default:
		return ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if strings.Count(path, "/") != 1 {
		return ""
	}
	return "https://github.com/" + path + ".git"
}

func sshIdentityAvailable() bool {
	if os.Getenv("SSH_AUTH_SOCK") != "" || os.Getenv("GIT_SSH_COMMAND") != "" || os.Getenv("GIT_SSH") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	keys, err := filepath.Glob(filepath.Join(home, ".ssh", "id_*"))
	return err == nil && len(keys) > 0
}

// DirectRepoURLFromGitHub is the remote a direct runner fetches for a GitHub
// "owner/repo" when the trigger recorded no clone URL: the https form, which
// needs no key for a public repository and uses a credential helper for a
// private one.
func DirectRepoURLFromGitHub(fullName string) string {
	if fullName == "" || strings.Contains(fullName, "://") || strings.HasPrefix(fullName, "git@") {
		return fullName
	}
	return "https://github.com/" + fullName + ".git"
}

// safety: Only transport fixtures may skip address validation; production fetches check every host.
// A zero timeout leaves ctx in charge.
type directOptions struct {
	protocols    string
	sourceRef    string
	lookup       sourceurl.Lookup
	fetchTimeout time.Duration
	cred         DirectCredential
	// safety: Cloud runners cannot write released deploy keys to disk.
	keyTmpfsOnly bool
}

func defaultDirectOptions() directOptions {
	return directOptions{
		protocols:    directProtocols,
		lookup:       net.DefaultResolver.LookupIPAddr,
		fetchTimeout: 10 * time.Minute,
	}
}

func fetchPipelineSourceDirect(ctx context.Context, repoURL, branch, sha, workDir string, opts directOptions) (string, error) {
	checkout, err := fetchRepositorySourceDirect(ctx, repoURL, branch, sha, workDir, opts)
	if err != nil {
		return "", err
	}
	return directSparkwingDir(checkout)
}

func fetchRepositorySourceDirect(ctx context.Context, repoURL, branch, sha, workDir string, opts directOptions) (string, error) {
	remote, sha, err := ValidateDirectSource(repoURL, sha)
	if err != nil {
		return "", err
	}
	if remote, err = credentialRemote(remote, opts.cred); err != nil {
		return "", err
	}
	if sha == "" && opts.sourceRef == "" {
		if branch == "" {
			branch = "main"
		}
		if !validBranchName(ctx, branch) {
			return "", fmt.Errorf("direct source: %q is not a branch name", branch)
		}
	}
	if err := fssecure.EnsureDir(workDir); err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	checkout := filepath.Join(workDir, "src")
	if err := directCheckout(ctx, remote, branch, sha, checkout, opts); err != nil {
		return "", err
	}
	return checkout, nil
}

// safety: A repository may commit .sparkwing as a symlink outside the checkout; never compile or run it.
func directSparkwingDir(checkout string) (string, error) {
	candidate := filepath.Join(checkout, ".sparkwing")
	fi, err := os.Lstat(candidate)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("fetched tree has no .sparkwing directory under %s", checkout)
	}
	realCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	if rel, relErr := filepath.Rel(realCheckout, realCandidate); relErr != nil || rel != ".sparkwing" {
		return "", fmt.Errorf("fetched .sparkwing resolves outside the checkout %s", checkout)
	}
	return candidate, nil
}

func validBranchName(ctx context.Context, branch string) bool {
	if strings.HasPrefix(branch, "-") {
		return false
	}
	return exec.CommandContext(ctx, "git", "check-ref-format", "--branch", branch).Run() == nil
}

func directCheckout(ctx context.Context, remote, branch, sha, dest string, opts directOptions) (err error) {
	defer func() { err = redactCredential(err, opts.cred) }()
	ref := "refs/heads/" + branch
	if opts.sourceRef != "" {
		if sha != "" {
			return errors.New("direct source: a declared ref and commit cannot both select the checkout")
		}
		ref, err = normalizeDirectRef(ctx, opts.sourceRef)
		if err != nil {
			return err
		}
		if directCommitRE.MatchString(ref) {
			sha, ref = ref, ""
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		return fmt.Errorf("direct source: create checkout: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dest))
		}
	}()

	fetchEnv := append(directGitEnv(os.Environ()), "GIT_ALLOW_PROTOCOL="+opts.protocols, "GIT_TERMINAL_PROMPT=0")
	localEnv := directLocalGitEnv(fetchEnv)
	if !opts.cred.Empty() {
		fetchEnv = credentialFetchEnv(localEnv)
	}
	// safety: only the fetch sees the helper and the pipe; every command that
	// touches the checkout runs with localEnv, which reads no
	// config from the environment at all.
	pipeCred := opts.cred.Kind == CredentialGitHubApp || opts.cred.Kind == CredentialHTTPS
	if pipeCred {
		scope, err := credentialScope(remote)
		if err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
		fetchEnv = withPipeCredential(fetchEnv, scope)
	}
	runWith := func(ctx context.Context, env []string, extra []*os.File, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = env
		cmd.ExtraFiles = extra
		killGroupOnCancel(cmd)
		out, err := gitCommandCombinedOutput(cmd)
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	run := func(ctx context.Context, env []string, args ...string) (string, error) {
		return runWith(ctx, env, nil, args...)
	}
	git := func(args ...string) (string, error) { return run(ctx, localEnv, args...) }
	// safety: an ambient template can install hooks or config before checkout.
	if _, err := git("init", "--quiet", "--template=", "--", dest); err != nil {
		return fmt.Errorf("direct source: %w", err)
	}
	if _, err := git("-C", dest, "remote", "add", "origin", remote); err != nil {
		return fmt.Errorf("direct source: %w", err)
	}

	var sshCommand string
	switch {
	case opts.cred.Kind == CredentialSSH:
		_, command, cleanup, err := writeSSHCredential(opts.cred, opts.keyTmpfsOnly)
		if err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
		// safety: the key leaves the disk when the checkout returns, before
		// the runner compiles or runs anything the fetched tree names; a key
		// that cannot be removed fails the checkout.
		defer func() {
			if rmErr := cleanup(); rmErr != nil {
				err = errors.Join(err, fmt.Errorf("direct source: remove the deploy key: %w", rmErr))
			}
		}()
		sshCommand = command
	case !opts.cred.Empty():
		sshCommand = "ssh" + directSSHOptions
	default:
		configured, err := run(ctx, fetchEnv, "-C", dest, "config", "--get", "core.sshCommand")
		// safety: git exits 1 when the key is unset; any other failure is a config the fetch cannot read either.
		var exitErr *exec.ExitError
		if err != nil && (!errors.As(err, &exitErr) || exitErr.ExitCode() != 1) {
			return fmt.Errorf("direct source: %w", err)
		}
		sshCommand = directSSHCommand(fetchEnv, configured)
	}
	fetchEnv = append(fetchEnv, "GIT_SSH_COMMAND="+sshCommand)
	// safety: a redirect would carry the fetch, and any credential a helper
	// hands it, to a host nothing here checked.
	fetchRef := func(ref string) error {
		fetchCtx := ctx
		if opts.fetchTimeout > 0 {
			var cancel context.CancelFunc
			fetchCtx, cancel = context.WithTimeout(ctx, opts.fetchTimeout)
			defer cancel()
		}
		var extra []*os.File
		env := fetchEnv
		cleanup := func() error { return nil }
		if pipeCred {
			var err error
			env, extra, cleanup, err = prepareCredentialTransport(env, opts.cred.Username, opts.cred.Secret, fetchCredentialAsks)
			if err != nil {
				return fmt.Errorf("source credential transport: %w", err)
			}
		}
		_, err := runWith(fetchCtx, env, extra, "-C", dest, "-c", "http.followRedirects=false",
			"fetch", "--quiet", "--no-tags", "--depth", "1", "--", remote, ref)
		err = errors.Join(err, cleanup())
		if err != nil && ctx.Err() == nil && errors.Is(fetchCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s", opts.fetchTimeout)
		}
		return err
	}

	target := sha
	if opts.lookup != nil {
		if err := sourceurl.CheckResolvedHost(ctx, remote, opts.lookup); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	}
	if sha == "" {
		if err := fetchRef(ref); err != nil {
			return fmt.Errorf("direct source: fetch %s ref %s: %w", sourceurl.Redact(remote), ref, err)
		}
		if target, err = git("-C", dest, "rev-parse", "--verify", "FETCH_HEAD^{commit}"); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	} else {
		if err := fetchRef(sha); err != nil {
			return fmt.Errorf("direct source: fetch %s at %s (is the commit pushed?): %w",
				sourceurl.Redact(remote), sha, err)
		}
	}
	size, err := directDirSize(filepath.Join(dest, ".git"))
	if err != nil {
		return err
	}
	if size > MaxSourceBytes {
		return fmt.Errorf("direct source: fetched Git data takes %d bytes, over the %d byte cap", size, MaxSourceBytes)
	}
	// safety: checkout sees no ambient config, hooks or template-installed code.
	if _, err := git("-C", dest, "-c", "core.hooksPath="+os.DevNull,
		"checkout", "--quiet", "--detach", target, "--"); err != nil {
		return fmt.Errorf("direct source: check out %s: %w", target, err)
	}
	return nil
}

func normalizeDirectRef(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if directCommitRE.MatchString(ref) {
		return strings.ToLower(ref), nil
	}
	if strings.HasPrefix(ref, "refs/remotes/origin/") {
		ref = "refs/heads/" + strings.TrimPrefix(ref, "refs/remotes/origin/")
	} else if strings.HasPrefix(ref, "origin/") {
		ref = "refs/heads/" + strings.TrimPrefix(ref, "origin/")
	}
	if (!strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/")) ||
		exec.CommandContext(ctx, "git", "check-ref-format", ref).Run() != nil {
		return "", fmt.Errorf("direct source: invalid declared ref %q", ref)
	}
	return ref, nil
}

// safety: Drop git variables that redirect every command or trace credentials during a direct fetch.
func directGitEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_ALLOW_PROTOCOL",
			"GIT_CURL_VERBOSE":
			continue
		}
		// safety: git's traces write request headers and credential exchanges
		// to wherever they point, which is how a source token would reach a log.
		if strings.HasPrefix(name, "GIT_TRACE") {
			continue
		}
		out = append(out, item)
	}
	return out
}

// NonInteractiveGitEnv is base for a git command that reaches a remote with
// no one to answer it: the variables that redirect git or write its traces
// are dropped, as for a source fetch, and no terminal, askpass helper or ssh
// dialog may prompt.
func NonInteractiveGitEnv(base []string) []string {
	env := directGitEnv(base)
	ssh := directSSHCommand(env, "")
	env = slices.DeleteFunc(env, func(item string) bool {
		name, _, _ := strings.Cut(item, "=")
		return name == "GIT_ASKPASS" || name == "SSH_ASKPASS" || name == "GIT_SSH_COMMAND" || name == "GIT_TERMINAL_PROMPT"
	})
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_SSH_COMMAND="+ssh)
}

// safety: SSH must neither prompt nor lend the fetched host this process's agent or ports.
const directSSHOptions = " -o BatchMode=yes -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes"

// safety: Git's configured SSH command may set options first; ssh keeps those first values.
// Unsupported clients fail on the appended restrictions rather than running without them.
func directSSHCommand(env []string, configured string) string {
	lookup := func(key string) string {
		value := ""
		for _, item := range env {
			if name, v, ok := strings.Cut(item, "="); ok && name == key {
				value = v
			}
		}
		return value
	}
	base := lookup("GIT_SSH_COMMAND")
	if base == "" {
		base = configured
	}
	if base == "" {
		if program := lookup("GIT_SSH"); program != "" {
			base = "'" + strings.ReplaceAll(program, "'", `'\''`) + "'"
		}
	}
	if base == "" {
		base = "ssh"
	}
	return base + directSSHOptions
}

// safety: A fetched .gitattributes may invoke ambient filter commands, so local git work reads no host config.
func directLocalGitEnv(fetchEnv []string) []string {
	out := make([]string, 0, len(fetchEnv)+3)
	for _, item := range fetchEnv {
		name, _, _ := strings.Cut(item, "=")
		switch {
		case name == "GIT_CONFIG_GLOBAL", name == "GIT_CONFIG_SYSTEM", name == "GIT_CONFIG_NOSYSTEM",
			name == "GIT_CONFIG", name == "GIT_CONFIG_PARAMETERS", name == "GIT_CONFIG_COUNT",
			strings.HasPrefix(name, "GIT_CONFIG_KEY_"), strings.HasPrefix(name, "GIT_CONFIG_VALUE_"):
			continue
		}
		out = append(out, item)
	}
	return append(out, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_LFS_SKIP_SMUDGE=1")
}

// RepoURLFromGitHub is the clone URL recorded for a GitHub repository name.
func RepoURLFromGitHub(fullName string) string {
	if fullName == "" {
		return ""
	}
	if strings.Contains(fullName, "://") || strings.HasPrefix(fullName, "git@") {
		return fullName
	}
	return "git@github.com:" + fullName + ".git"
}

// TriggerRepoURL resolves the one repository named by a trigger and selects
// the transport this machine can use for a direct fetch.
func TriggerRepoURL(repoURL, githubRepository, githubOwner, githubRepo string) (string, error) {
	if _, err := sourceurl.TriggerRepository(repoURL, githubRepository, githubOwner, githubRepo); err != nil {
		return "", err
	}
	slug := githubRepository
	if slug == "" && githubOwner != "" {
		slug = githubOwner + "/" + githubRepo
	}
	raw := repoURL
	if raw == "" {
		raw = DirectRepoURLFromGitHub(slug)
	}
	if raw == "" {
		return "", nil
	}
	return sourceurl.ValidateCloneURL(DirectFetchURL(raw))
}

func directDirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path != dir && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				if errors.Is(infoErr, fs.ErrNotExist) {
					return nil
				}
				return infoErr
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
