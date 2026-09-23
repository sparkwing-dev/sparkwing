package bincache

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// directProtocols is every transport a direct fetch may use. git checks it
// after insteadOf rewriting, so an ambient url.*.insteadOf cannot reach
// file://, ext:: or a local path either.
const directProtocols = "https:ssh"

var directCommitRE = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ErrWorkspaceNeedsCache reports a working-tree snapshot trigger on a runner
// without the operator's cache: the snapshot commit exists only there.
var ErrWorkspaceNeedsCache = errors.New("working-tree snapshots are served only by the operator's git cache; " +
	"push your commit and trigger without --working-tree, since team runs fetch from the remote")

// ValidateDirectSource checks what a direct fetch hands git: an https or ssh
// remote with no credential in it (sourceurl.ValidateCloneURL refuses every
// other scheme, a local path, userinfo on https and a leading dash), and a
// full commit id when one is given.
func ValidateDirectSource(repoURL, sha string) (string, string, error) {
	remote, err := sourceurl.ValidateCloneURL(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("direct source: %w", err)
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
	for _, name := range []string{"SSH_AUTH_SOCK", "GIT_SSH_COMMAND", "GIT_SSH"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	keys, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_*"))
	return len(keys) > 0
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

// FetchPipelineSourceDirect checks out repoURL at sha under workDir with this
// process's own git config and credentials, and returns the checkout's
// .sparkwing directory. Fetched objects stay in a mirror under the Sparkwing
// home keyed by the remote, so a later run of the same repository fetches only
// what it lacks. An empty sha takes the tip of branch.
func FetchPipelineSourceDirect(ctx context.Context, repoURL, branch, sha, workDir string) (string, error) {
	remote, sha, err := ValidateDirectSource(repoURL, sha)
	if err != nil {
		return "", err
	}
	if sha == "" {
		if branch == "" {
			branch = "main"
		}
		if !validBranchName(ctx, branch) {
			return "", fmt.Errorf("direct source: %q is not a branch name", branch)
		}
	}
	root := filepath.Join(SparkwingHome(), "source-direct")
	if err := fssecure.EnsureDir(root); err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	if err := fssecure.EnsureDir(workDir); err != nil {
		return "", fmt.Errorf("direct source: %w", err)
	}
	checkout := filepath.Join(workDir, "src")
	if err := directCheckout(ctx, root, remote, branch, sha, checkout, directProtocols); err != nil {
		return "", err
	}
	candidate := filepath.Join(checkout, ".sparkwing")
	if fi, statErr := os.Stat(candidate); statErr != nil || !fi.IsDir() {
		return "", fmt.Errorf("fetched tree has no .sparkwing directory under %s", checkout)
	}
	return candidate, nil
}

func validBranchName(ctx context.Context, branch string) bool {
	if strings.HasPrefix(branch, "-") {
		return false
	}
	return exec.CommandContext(ctx, "git", "check-ref-format", "--branch", branch).Run() == nil
}

func directMirrorPath(root, remote string) string {
	sum := sha256.Sum256([]byte(remote))
	return filepath.Join(root, fmt.Sprintf("%x.git", sum[:16]))
}

func directCheckout(ctx context.Context, root, remote, branch, sha, dest, protocols string) error {
	mirror := directMirrorPath(root, remote)
	lock, err := fssecure.OpenFile(mirror+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("direct source: open mirror lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	// safety: fetch, shallow bookkeeping and worktree registration all write the mirror.
	if _, err := cacheLock(lock, cacheLockExclusive); err != nil {
		return fmt.Errorf("direct source: lock mirror: %w", err)
	}
	defer func() { _ = cacheUnlock(lock) }()

	env := append(directGitEnv(os.Environ()), "GIT_ALLOW_PROTOCOL="+protocols, "GIT_TERMINAL_PROMPT=0")
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, statErr := os.Stat(filepath.Join(mirror, "HEAD")); statErr != nil {
		if err := os.RemoveAll(mirror); err != nil {
			return fmt.Errorf("direct source: clear mirror: %w", err)
		}
		if _, err := git("init", "--bare", "--quiet", mirror); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	}
	if _, err := git("-C", mirror, "worktree", "prune"); err != nil {
		return fmt.Errorf("direct source: %w", err)
	}

	target := sha
	if sha == "" {
		if _, err := git("-C", mirror, "fetch", "--quiet", "--no-tags", "--depth", "1", "--",
			remote, "refs/heads/"+branch); err != nil {
			return fmt.Errorf("direct source: fetch %s branch %s: %w", sourceurl.Redact(remote), branch, err)
		}
		if target, err = git("-C", mirror, "rev-parse", "--verify", "FETCH_HEAD^{commit}"); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	} else if _, haveErr := git("-C", mirror, "cat-file", "-e", sha+"^{commit}"); haveErr != nil {
		if _, err := git("-C", mirror, "fetch", "--quiet", "--no-tags", "--depth", "1", "--",
			remote, sha); err != nil {
			return fmt.Errorf("direct source: fetch %s at %s (is the commit pushed?): %w",
				sourceurl.Redact(remote), sha, err)
		}
	}
	// safety: the mirror was created here, so the only hooks a checkout could run
	// are ones ambient config points at, and a run's source is not the place for them.
	if _, err := git("-C", mirror, "-c", "core.hooksPath=/dev/null",
		"worktree", "add", "--quiet", "--detach", "--", dest, target); err != nil {
		return fmt.Errorf("direct source: check out %s: %w", target, err)
	}
	return nil
}

// directGitEnv keeps the ambient git config and credentials but drops the
// variables that would point every command at some other repository.
func directGitEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_ALLOW_PROTOCOL":
			continue
		}
		out = append(out, item)
	}
	return out
}

// TriggerRepoURL is the remote a runner fetches a trigger's source from, given
// the trigger's GitHub "owner/repo" and the clone URL it recorded. The git
// cache names a GitHub repository by the ssh form its webhook binding
// registers, so the cache path prefers the GitHub name. A direct fetch uses the
// recorded clone URL, the remote its author pushed to, and otherwise the GitHub
// name's https form; either way DirectFetchURL then fits it to the identities
// this process holds. An empty result means the trigger names no repository.
func TriggerRepoURL(githubRepository, repoURL string, direct bool) (string, error) {
	var raw string
	switch {
	case direct && repoURL != "":
		raw = repoURL
	case direct:
		raw = DirectRepoURLFromGitHub(githubRepository)
	case githubRepository != "":
		raw = RepoURLFromGitHub(githubRepository)
	default:
		raw = repoURL
	}
	if raw == "" {
		return "", nil
	}
	if direct {
		raw = DirectFetchURL(raw)
	}
	return sourceurl.ValidateCloneURL(raw)
}
