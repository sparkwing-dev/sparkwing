package bincache

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
	cleanup := func() error { return nil }
	full := []string{"-C", dir, "-c", "core.hooksPath=/dev/null"}
	if remote {
		if g.opts.fetchTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, g.opts.fetchTimeout)
			defer cancel()
		}
		// safety: a redirect would carry the credential to a host nothing checked.
		env, full = g.fetchEnv, append(full, "-c", "http.followRedirects=false", "-c", "protocol.file.allow=never")
		if g.pipe {
			var err error
			env, extra, cleanup, err = prepareCredentialTransport(env, g.cred.Username, g.cred.Secret, fetchCredentialAsks)
			if err != nil {
				return nil, fmt.Errorf("source credential transport: %w", err)
			}
		}
	}
	cmd := exec.CommandContext(ctx, "git", append(full, args...)...)
	cmd.Env = env
	cmd.ExtraFiles = extra
	killGroupOnCancel(cmd)
	out, err := gitCommandOutput(cmd)
	err = errors.Join(err, cleanup())
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
//
// submoduleRepos lists, as sourceurl.Identity values, the only repositories a
// submodule may name.
func CheckoutSource(ctx context.Context, repoURL, sha, branch, dest string, cred DirectCredential, o SourceOptions,
	submoduleRepos []string,
) error {
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
	return errors.Join(g.checkout(ctx, strings.ToLower(sha), branch, dest, o, submoduleRepos), g.close())
}

func (g *trustedGit) checkout(ctx context.Context, sha, branch, dest string, o SourceOptions, submoduleRepos []string) error {
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
		allowed := map[string]bool{}
		for _, id := range submoduleRepos {
			allowed[strings.ToLower(id)] = true
		}
		if err := g.submodules(ctx, dest, g.remote, allowed, 0); err != nil {
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

// safety: .gitmodules comes from the fetched tree and a host-wide credential reaches every repository on its
// host, so each level is read before it is fetched and every URL, relative ones resolved first, must name a
// repository in allowed, the owner's list for the run's repository.
func (g *trustedGit) submodules(ctx context.Context, dir, parentURL string, allowed map[string]bool, level int) error {
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
	type sub struct{ path, url string }
	subs := map[string]*sub{}
	for _, line := range strings.Split(out, "\n") {
		key, value, _ := strings.Cut(line, " ")
		name, field := key[:max(strings.LastIndex(key, "."), 0)], key[strings.LastIndex(key, ".")+1:]
		if subs[name] == nil {
			subs[name] = &sub{}
		}
		switch field {
		case "path":
			if !filepath.IsLocal(value) {
				return fmt.Errorf("source: submodule path %q leaves the checkout", value)
			}
			subs[name].path = value
		case "url":
			resolved, ok := resolveSubmoduleURL(parentURL, value)
			if id, idOK := repoIdentity(resolved); !ok || !idOK || !allowed[id] {
				return fmt.Errorf("source: submodule %s is not a repository the team's owner listed for this one",
					sourceurl.Redact(value))
			}
			subs[name].url = resolved
		}
	}
	scope, err := credentialScope(g.remote)
	if err != nil && g.pipe {
		return fmt.Errorf("source: %w", err)
	}
	if err := directSubmodules(ctx, dir, scope, g.cred, g.opts, false); err != nil {
		return err
	}
	for _, sm := range subs {
		if sm.path != "" && sm.url != "" {
			if err := g.submodules(ctx, filepath.Join(dir, sm.path), sm.url, allowed, level+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// safety: git resolves a relative URL against the parent's remote; one that climbs past the remote's path would
// leave its host, so it is refused rather than clamped.
func resolveSubmoduleURL(parent, raw string) (string, bool) {
	if !strings.HasPrefix(raw, "./") && !strings.HasPrefix(raw, "../") {
		return raw, true
	}
	base, rest, scp := parent, "", false
	if i := strings.Index(parent, "://"); i >= 0 {
		j := strings.IndexByte(parent[i+3:], '/')
		if j < 0 {
			return "", false
		}
		base, rest = parent[:i+3+j], parent[i+3+j:]
	} else if host, p, ok := strings.Cut(parent, ":"); ok {
		base, rest, scp = host+":", p, true
	}
	segs := strings.FieldsFunc(rest, func(r rune) bool { return r == '/' })
	for _, part := range strings.Split(raw, "/") {
		switch part {
		case ".", "":
		case "..":
			if len(segs) == 0 {
				return "", false
			}
			segs = segs[:len(segs)-1]
		default:
			segs = append(segs, part)
		}
	}
	if scp {
		return base + strings.Join(segs, "/"), true
	}
	return base + "/" + strings.Join(segs, "/"), true
}

// safety: matched against the owner's list in the form sourceurl.Identity gives: host with its port, lowercased
// path, no .git.
func repoIdentity(raw string) (string, bool) {
	var host, p string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", false
		}
		host, p = u.Host, u.Path
	} else {
		dest, rest, ok := strings.Cut(raw, ":")
		if !ok {
			return "", false
		}
		_, host, _ = strings.Cut(dest, "@")
		if host == "" {
			host = dest
		}
		p = rest
	}
	host = strings.TrimRight(strings.ToLower(host), ".")
	p = strings.Trim(strings.TrimSuffix(strings.ToLower(strings.Trim(p, "/")), ".git"), "/")
	if host == "" || p == "" || hasDotDot(p) {
		return "", false
	}
	return host + "/" + p, true
}

func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." || seg == "" {
			return true
		}
	}
	return false
}
