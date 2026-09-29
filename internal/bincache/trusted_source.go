package bincache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

// SourceOptions is what a pipeline asks of its checkout. The zero value is
// the direct fetch's shape: one commit, no tags, no submodules, and LFS
// files left as pointers.
type SourceOptions struct {
	// Depth is how many commits of history to fetch; 0 fetches all of it.
	Depth      int
	Tags       bool
	Submodules bool
	LFS        bool
}

// MaxSourceBytes caps a trusted checkout on disk, as the direct path caps
// its mirrors.
const MaxSourceBytes int64 = 10 << 30

// safety: a submodule nests another fetched tree, so recursion is bounded.
const maxSubmoduleLevels = 4

// safety: the credential reaches git only on an inherited pipe or a tmpfs key file, and no command
// reads this process's git config, so nothing the fetched tree names runs and the credential never lands in it.
type trustedGit struct {
	remote   string
	cred     DirectCredential
	opts     directOptions
	fetchEnv []string
	localEnv []string
	pipe     bool
	cleanup  func() error
}

func newTrustedGit(ctx context.Context, repoURL string, cred DirectCredential) (*trustedGit, error) {
	if cred.Empty() {
		return nil, errors.New("source: a trusted fetch presents only a released credential")
	}
	remote, _, err := ValidateDirectSource(repoURL, "")
	if err != nil {
		return nil, err
	}
	if remote, err = credentialRemote(remote, cred); err != nil {
		return nil, err
	}
	opts := defaultDirectOptions()
	if err := sourceurl.CheckResolvedHost(ctx, remote, opts.lookup); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	return openTrustedGit(remote, cred, opts)
}

func openTrustedGit(remote string, cred DirectCredential, opts directOptions) (*trustedGit, error) {
	base := append(directGitEnv(os.Environ()), "GIT_ALLOW_PROTOCOL="+opts.protocols, "GIT_TERMINAL_PROMPT=0")
	g := &trustedGit{remote: remote, cred: cred, opts: opts, localEnv: directLocalGitEnv(base), cleanup: func() error { return nil }}
	g.fetchEnv = credentialFetchEnv(g.localEnv)
	switch cred.Kind {
	case CredentialGitHubApp, CredentialHTTPS:
		scope, err := credentialScope(remote)
		if err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		g.pipe = true
		g.fetchEnv = append(withPipeCredential(g.fetchEnv, scope), "GIT_SSH_COMMAND=ssh"+directSSHOptions)
	case CredentialSSH:
		_, command, cleanup, err := writeSSHCredential(cred, false)
		if err != nil {
			return nil, fmt.Errorf("source: %w", err)
		}
		g.cleanup = cleanup
		g.fetchEnv = append(g.fetchEnv, "GIT_SSH_COMMAND="+command)
	default:
		return nil, fmt.Errorf("source: unknown credential kind %q", cred.Kind)
	}
	return g, nil
}

func (g *trustedGit) close() error { return g.cleanup() }

// safety: only a command that talks to the remote presents the credential.
func (g *trustedGit) git(ctx context.Context, dir string, remote bool, args ...string) (string, error) {
	out, err := g.gitRaw(ctx, dir, remote, args...)
	return strings.TrimSpace(string(out)), err
}

func (g *trustedGit) gitRaw(ctx context.Context, dir string, remote bool, args ...string) ([]byte, error) {
	env, extra := g.localEnv, []*os.File(nil)
	full := []string{"-C", dir, "-c", "core.hooksPath=/dev/null"}
	if remote {
		if g.opts.fetchTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, g.opts.fetchTimeout)
			defer cancel()
		}
		if g.pipe {
			pipe, err := credentialPipe(g.cred.Username, g.cred.Secret, fetchCredentialAsks)
			if err != nil {
				return nil, fmt.Errorf("source credential pipe: %w", err)
			}
			defer func() { _ = pipe.Close() }()
			extra = []*os.File{pipe}
		}
		// safety: a redirect would carry the credential to a host nothing checked.
		env, full = g.fetchEnv, append(full, "-c", "http.followRedirects=false", "-c", "protocol.file.allow=never")
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = env
	cmd.ExtraFiles = extra
	killGroupOnCancel(cmd)
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return nil, redactCredential(fmt.Errorf("git %s: %w", args[0], err), g.cred)
	}
	return out, nil
}

// CheckoutSource checks out sha of repoURL into dest, a new directory, as a
// standalone repository with its .git: origin names the remote with no
// credential in it, and HEAD is branch at sha, or sha detached when branch is
// empty. cred must be a credential the controller holds for the repository.
func CheckoutSource(ctx context.Context, repoURL, sha, branch, dest string, cred DirectCredential, o SourceOptions) error {
	if _, _, err := ValidateDirectSource(repoURL, sha); err != nil || sha == "" {
		return errors.Join(errors.New("source: a trusted checkout needs the run's full commit id"), err)
	}
	if branch != "" && !validBranchName(ctx, branch) {
		branch = ""
	}
	g, err := newTrustedGit(ctx, repoURL, cred)
	if err != nil {
		return err
	}
	return errors.Join(g.checkout(ctx, strings.ToLower(sha), branch, dest, o), g.close())
}

func (g *trustedGit) checkout(ctx context.Context, sha, branch, dest string, o SourceOptions) error {
	if o.Depth < 0 {
		return errors.New("source: depth must be 0 (all history) or more")
	}
	// safety: an LFS server's batch answer names the hosts its objects come
	// from, so only GitHub's, which the App token reaches, is followed.
	if o.LFS && g.cred.Kind != CredentialGitHubApp {
		return errors.New("source: LFS objects are fetched only through the team's GitHub App")
	}
	if _, err := g.git(ctx, filepath.Dir(dest), false, "init", "--quiet", "--", dest); err != nil {
		return err
	}
	if _, err := g.git(ctx, dest, false, "remote", "add", "origin", g.remote); err != nil {
		return err
	}
	fetch := []string{"fetch", "--quiet", "--no-tags"}
	if o.Tags {
		fetch[2] = "--tags"
	}
	if o.Depth > 0 {
		fetch = append(fetch, "--depth", strconv.Itoa(o.Depth))
	}
	if _, err := g.git(ctx, dest, true, append(fetch, "--", "origin", sha)...); err != nil {
		return fmt.Errorf("source: fetch %s at %s (is the commit pushed?): %w", sourceurl.Redact(g.remote), sha, err)
	}
	co := []string{"checkout", "--quiet", "--detach", sha}
	if branch != "" {
		co = []string{"checkout", "--quiet", "-B", branch, sha}
	}
	if _, err := g.git(ctx, dest, false, co...); err != nil {
		return err
	}
	if o.Submodules {
		if err := g.submodules(ctx, dest, 0); err != nil {
			return err
		}
	}
	if o.LFS {
		// safety: git config outranks a committed .lfsconfig, so the tree cannot
		// point the fetch, and the credential, at an endpoint of its choosing.
		if _, err := g.git(ctx, dest, false, "config", "lfs.url", strings.TrimSuffix(g.remote, "/")+"/info/lfs"); err != nil {
			return err
		}
		if _, err := g.git(ctx, dest, true, "lfs", "pull", "origin"); err != nil {
			return fmt.Errorf("source: fetch LFS objects: %w", err)
		}
	}
	size, err := directDirSize(dest)
	if err != nil {
		return err
	}
	if size > MaxSourceBytes {
		return fmt.Errorf("source: the checkout takes %d bytes, over the %d byte cap", size, MaxSourceBytes)
	}
	return nil
}

// safety: .gitmodules comes from the fetched tree, so every level is read
// before it is fetched and each URL must name the credential's own host; a
// relative URL resolves against origin, which already does.
func (g *trustedGit) submodules(ctx context.Context, dir string, level int) error {
	if !hasGitmodules(dir) {
		return nil
	}
	if level >= maxSubmoduleLevels {
		return fmt.Errorf("source: submodules nest deeper than %d levels", maxSubmoduleLevels)
	}
	out, err := g.git(ctx, dir, false, "config", "-f", ".gitmodules", "--get-regexp", `^submodule\..*\.(url|path)$`)
	if err != nil {
		return fmt.Errorf("source: read .gitmodules: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch {
		case strings.HasSuffix(key, ".path"):
			if !filepath.IsLocal(value) {
				return fmt.Errorf("source: submodule path %q leaves the checkout", value)
			}
			paths = append(paths, value)
		case strings.HasSuffix(key, ".url") && !strings.HasPrefix(value, "./") && !strings.HasPrefix(value, "../"):
			if host, err := sourceurl.Host(value); err != nil || !strings.EqualFold(host, g.cred.Host) {
				return fmt.Errorf("source: submodule %s is not on %s, the host the run's credential reaches",
					sourceurl.Redact(value), g.cred.Host)
			}
		}
	}
	scope, err := credentialScope(g.remote)
	if err != nil && g.pipe {
		return fmt.Errorf("source: %w", err)
	}
	if err := directSubmodules(ctx, dir, scope, g.cred, g.opts, false); err != nil {
		return err
	}
	for _, p := range paths {
		if err := g.submodules(ctx, filepath.Join(dir, p), level+1); err != nil {
			return err
		}
	}
	return nil
}

// ModuleCommit is one revision of a Go module's repository, fetched into
// Dir, a working repository that has the commit's whole tree.
type ModuleCommit struct {
	Dir    string
	Commit string
	Time   time.Time
}

// FetchModuleCommit fetches rev of repoURL into dir, a new directory. rev is
// a full ref, or the 12-digit commit prefix a pseudo-version carries, which
// is resolved against the repository's branches and tags.
func FetchModuleCommit(ctx context.Context, repoURL, rev, dir string, cred DirectCredential) (ModuleCommit, error) {
	g, err := newTrustedGit(ctx, repoURL, cred)
	if err != nil {
		return ModuleCommit{}, err
	}
	m, err := g.moduleCommit(ctx, rev, dir)
	return m, errors.Join(err, g.close())
}

func (g *trustedGit) moduleCommit(ctx context.Context, rev, dir string) (ModuleCommit, error) {
	want := rev
	if !strings.HasPrefix(rev, "refs/") {
		// safety: a server serves only whole commit ids, so a prefix is resolved
		// against a commits-only fetch kept apart from the repository archived.
		probe := filepath.Join(dir, "probe")
		if _, err := g.git(ctx, dir, false, "init", "--quiet", "--bare", "--", probe); err != nil {
			return ModuleCommit{}, err
		}
		if _, err := g.git(ctx, probe, true, "fetch", "--quiet", "--no-tags", "--filter=tree:0", "--", g.remote,
			"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			return ModuleCommit{}, err
		}
		full, err := g.git(ctx, probe, false, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
		if err != nil {
			return ModuleCommit{}, fmt.Errorf("no commit %s on any branch or tag", rev)
		}
		want = full
	}
	repo := filepath.Join(dir, "repo")
	if _, err := g.git(ctx, dir, false, "init", "--quiet", "--", repo); err != nil {
		return ModuleCommit{}, err
	}
	if _, err := g.git(ctx, repo, true, "fetch", "--quiet", "--no-tags", "--depth", "1", "--", g.remote, want); err != nil {
		return ModuleCommit{}, err
	}
	out, err := g.git(ctx, repo, false, "log", "-1", "--format=%H %ct", "FETCH_HEAD")
	if err != nil {
		return ModuleCommit{}, err
	}
	commit, secs, _ := strings.Cut(out, " ")
	unix, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return ModuleCommit{}, fmt.Errorf("read commit time: %w", err)
	}
	return ModuleCommit{Dir: repo, Commit: commit, Time: time.Unix(unix, 0).UTC()}, nil
}

// File returns path at the commit, and false when the commit has no such file.
func (m ModuleCommit) File(ctx context.Context, path string) ([]byte, bool, error) {
	g := &trustedGit{localEnv: directLocalGitEnv(directGitEnv(os.Environ()))}
	if entry, err := g.git(ctx, m.Dir, false, "ls-tree", m.Commit, "--", path); err != nil || entry == "" {
		return nil, false, err
	}
	out, err := g.gitRaw(ctx, m.Dir, false, "cat-file", "blob", m.Commit+":"+path)
	return out, err == nil, err
}

// ModuleTags lists repoURL's tags whose names start with prefix.
func ModuleTags(ctx context.Context, repoURL, prefix, dir string, cred DirectCredential) ([]string, error) {
	g, err := newTrustedGit(ctx, repoURL, cred)
	if err != nil {
		return nil, err
	}
	tags, err := g.tags(ctx, prefix, dir)
	return tags, errors.Join(err, g.close())
}

func (g *trustedGit) tags(ctx context.Context, prefix, dir string) ([]string, error) {
	out, err := g.git(ctx, dir, true, "ls-remote", "--tags", "--refs", "--", g.remote, "refs/tags/"+prefix+"*")
	if err != nil {
		return nil, err
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			tags = append(tags, strings.TrimPrefix(ref, "refs/tags/"))
		}
	}
	return tags, nil
}
