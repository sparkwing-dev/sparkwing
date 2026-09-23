package bincache

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	// safety: no forge needs one, and each spelling of one would get its own mirror.
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

// FetchPipelineSourceDirect checks out repoURL at sha under workDir and
// returns the checkout's .sparkwing directory. With a credential the fetch
// presents only that credential and reads none of this machine's git config,
// ssh agent or keys; the zero credential fetches with this process's own git
// config and credentials, which only an owner-fenced runner may do. Fetched
// objects stay in a mirror under the Sparkwing home keyed by the remote, so a
// later run of the same repository fetches only what it lacks. An empty sha
// takes the tip of branch.
func FetchPipelineSourceDirect(ctx context.Context, repoURL, branch, sha, workDir string, cred DirectCredential) (string, error) {
	opts := defaultDirectOptions()
	opts.cred = cred
	return fetchPipelineSourceDirect(ctx, repoURL, branch, sha, workDir, opts)
}

// directOptions bound a direct fetch. A nil lookup skips the address check,
// which only a test serving from loopback wants; a zero fetchTimeout leaves the
// fetch to ctx, and a zero cap is no cap. maxMirrorBytes bounds all mirrors
// under one Sparkwing home together.
type directOptions struct {
	protocols      string
	lookup         sourceurl.Lookup
	fetchTimeout   time.Duration
	maxMirrors     int
	maxMirrorBytes int64
	cred           DirectCredential
}

func defaultDirectOptions() directOptions {
	return directOptions{
		protocols:      directProtocols,
		lookup:         net.DefaultResolver.LookupIPAddr,
		fetchTimeout:   10 * time.Minute,
		maxMirrors:     20,
		maxMirrorBytes: 10 << 30,
	}
}

func fetchPipelineSourceDirect(ctx context.Context, repoURL, branch, sha, workDir string, opts directOptions) (string, error) {
	remote, sha, err := ValidateDirectSource(repoURL, sha)
	if err != nil {
		return "", err
	}
	if remote, err = credentialRemote(remote, opts.cred); err != nil {
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
	if err := directCheckout(ctx, root, remote, branch, sha, checkout, opts); err != nil {
		return "", err
	}
	return directSparkwingDir(checkout)
}

// directSparkwingDir is the checkout's .sparkwing directory. A repository can
// commit .sparkwing as a symlink to any path on the runner, which the build
// would then compile and run, so only a real directory inside the checkout
// counts.
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

func directMirrorPath(root, remote string) string {
	sum := sha256.Sum256([]byte(directMirrorKey(remote)))
	return filepath.Join(root, fmt.Sprintf("%x.git", sum[:16]))
}

// directMirrorKey folds the spellings every forge treats as one repository,
// host case and a trailing ".git" or "/", so they share one mirror. The path
// keeps its case, which some servers honor.
func directMirrorKey(remote string) string {
	trimPath := func(path string) string {
		return strings.TrimSuffix(strings.TrimRight(path, "/"), ".git")
	}
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		u.Scheme, u.Host, u.Path, u.RawPath = strings.ToLower(u.Scheme), strings.ToLower(u.Host), trimPath(u.Path), ""
		return u.String()
	}
	dest, path, _ := strings.Cut(remote, ":")
	user, host, found := strings.Cut(dest, "@")
	if !found {
		return strings.ToLower(dest) + ":" + trimPath(path)
	}
	return user + "@" + strings.ToLower(host) + ":" + trimPath(path)
}

func directCheckout(ctx context.Context, root, remote, branch, sha, dest string, opts directOptions) (err error) {
	defer func() { err = redactCredential(err, opts.cred) }()
	mirror := directMirrorPath(root, remote)
	lock, err := fssecure.OpenFile(mirror+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("direct source: open mirror lock: %w", err)
	}
	// safety: closing the file releases the lock.
	defer func() { _ = lock.Close() }()
	// safety: fetch, shallow bookkeeping and worktree registration all write the mirror.
	if _, err := cacheLock(lock, cacheLockExclusive); err != nil {
		return fmt.Errorf("direct source: lock mirror: %w", err)
	}

	fetchEnv := append(directGitEnv(os.Environ()), "GIT_ALLOW_PROTOCOL="+opts.protocols, "GIT_TERMINAL_PROMPT=0")
	localEnv := directLocalGitEnv(fetchEnv)
	if !opts.cred.Empty() {
		fetchEnv = credentialFetchEnv(localEnv)
	}
	// safety: only the fetch sees the helper and the pipe; every command that
	// touches the mirror or the checkout runs with localEnv, which reads no
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
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	run := func(ctx context.Context, env []string, args ...string) (string, error) {
		return runWith(ctx, env, nil, args...)
	}
	git := func(args ...string) (string, error) { return run(ctx, localEnv, args...) }
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
	now := time.Now()
	if err := os.Chtimes(mirror, now, now); err != nil {
		return fmt.Errorf("direct source: mark mirror used: %w", err)
	}

	var sshCommand string
	switch {
	case opts.cred.Kind == CredentialSSH:
		keyDir, command, err := writeSSHCredential(opts.cred)
		if err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
		// safety: the key leaves the disk when the checkout returns, before
		// the runner compiles or runs anything the fetched tree names; a key
		// that cannot be removed fails the checkout.
		defer func() {
			if rmErr := os.RemoveAll(keyDir); rmErr != nil {
				err = errors.Join(err, fmt.Errorf("direct source: remove the deploy key: %w", rmErr))
			}
		}()
		sshCommand = command
	case !opts.cred.Empty():
		sshCommand = "ssh" + directSSHOptions
	default:
		configured, err := run(ctx, fetchEnv, "-C", mirror, "config", "--get", "core.sshCommand")
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
		if pipeCred {
			cred, err := credentialPipe(opts.cred.Username, opts.cred.Secret, fetchCredentialAsks)
			if err != nil {
				return fmt.Errorf("source credential pipe: %w", err)
			}
			defer func() { _ = cred.Close() }()
			extra = []*os.File{cred}
		}
		_, err := runWith(fetchCtx, fetchEnv, extra, "-C", mirror, "-c", "http.followRedirects=false",
			"fetch", "--quiet", "--no-tags", "--depth", "1", "--", remote, ref)
		if err != nil && ctx.Err() == nil && errors.Is(fetchCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s", opts.fetchTimeout)
		}
		return err
	}

	target := sha
	needFetch := sha == ""
	if !needFetch {
		_, haveErr := git("-C", mirror, "cat-file", "-e", sha+"^{commit}")
		needFetch = haveErr != nil
	}
	if needFetch && opts.lookup != nil {
		if err := sourceurl.CheckResolvedHost(ctx, remote, opts.lookup); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	}
	if sha == "" {
		if err := fetchRef("refs/heads/" + branch); err != nil {
			return fmt.Errorf("direct source: fetch %s branch %s: %w", sourceurl.Redact(remote), branch, err)
		}
		if target, err = git("-C", mirror, "rev-parse", "--verify", "FETCH_HEAD^{commit}"); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	} else if needFetch {
		if err := fetchRef(sha); err != nil {
			return fmt.Errorf("direct source: fetch %s at %s (is the commit pushed?): %w",
				sourceurl.Redact(remote), sha, err)
		}
	}
	if err := directEnforceCaps(root, mirror, opts, git); err != nil {
		return err
	}
	// safety: the mirror was created here and the checkout sees no ambient config,
	// so the tree's hooks and attributes name nothing that can run.
	if _, err := git("-C", mirror, "-c", "core.hooksPath=/dev/null",
		"worktree", "add", "--quiet", "--detach", "--", dest, target); err != nil {
		return fmt.Errorf("direct source: check out %s: %w", target, err)
	}
	return nil
}

// directGitEnv keeps the ambient git config and credentials but drops the
// variables that would point every command at some other repository or trace
// what it sends.
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

// directSSHOptions keep an ssh fetch from prompting, trusting a host key it
// has not seen, or lending the fetched host this process's agent or ports.
const directSSHOptions = " -o BatchMode=yes -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes"

// directSSHCommand is the GIT_SSH_COMMAND a fetch runs: the ssh command git
// would otherwise have picked from env and configured (core.sshCommand), in
// git's own order, with directSSHOptions appended. ssh keeps the first value
// it reads for an option, so an -o the user's own command already passes wins;
// a non-OpenSSH program such as plink fails on the options rather than
// running unhardened.
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

// directLocalGitEnv is fetchEnv for every git command that touches only the
// mirror or the checkout. A fetched tree's .gitattributes can name any filter
// driver, and the ambient config may define one with a smudge or process
// command, so these commands read no system, global or environment config at
// all rather than trying to name every key that runs something.
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

// TriggerRepoURL is the remote a runner fetches a trigger's source from, given
// every name the trigger recorded for its repository: the clone URL,
// GITHUB_REPOSITORY and github_owner/github_repo. They must all name one
// repository (sourceurl.TriggerRepository), so the cache path and the direct
// path fetch the same one and differ only in the form their transport needs:
// the git cache names a GitHub repository by the ssh form its webhook binding
// registers, and a direct fetch uses the recorded clone URL, else the GitHub
// name's https form, fitted by DirectFetchURL to the identities this process
// holds. An empty result means the trigger names no repository.
func TriggerRepoURL(repoURL, githubRepository, githubOwner, githubRepo string, direct bool) (string, error) {
	if _, err := sourceurl.TriggerRepository(repoURL, githubRepository, githubOwner, githubRepo); err != nil {
		return "", err
	}
	slug := githubRepository
	if slug == "" && githubOwner != "" {
		slug = githubOwner + "/" + githubRepo
	}
	var raw string
	switch {
	case direct && repoURL != "":
		raw = repoURL
	case direct:
		raw = DirectRepoURLFromGitHub(slug)
	case slug != "":
		raw = RepoURLFromGitHub(slug)
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

// directEnforceCaps keeps the mirrors under root within opts' count and byte
// caps, removing the least recently used ones no run holds. git cannot bound
// what a fetch writes, so a mirror that alone exceeds the byte cap is removed
// after the fetch and its checkout refused.
func directEnforceCaps(root, own string, opts directOptions, git func(...string) (string, error)) error {
	if opts.maxMirrors <= 0 && opts.maxMirrorBytes <= 0 {
		return nil
	}
	ownSize, err := directDirSize(own)
	if err != nil {
		return fmt.Errorf("direct source: size mirror: %w", err)
	}
	if opts.maxMirrorBytes > 0 && ownSize > opts.maxMirrorBytes {
		if err := os.RemoveAll(own); err != nil {
			return fmt.Errorf("direct source: remove oversized mirror: %w", err)
		}
		return fmt.Errorf("direct source: the fetched repository takes %d bytes, over the %d byte mirror cap",
			ownSize, opts.maxMirrorBytes)
	}
	type mirrorUse struct {
		path string
		used time.Time
		size int64
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.git"))
	if err != nil {
		return fmt.Errorf("direct source: list mirrors: %w", err)
	}
	var others []mirrorUse
	total := ownSize
	for _, path := range paths {
		fi, statErr := os.Stat(path)
		if path == own || statErr != nil || !fi.IsDir() {
			continue
		}
		size, sizeErr := directDirSize(path)
		if sizeErr != nil {
			continue
		}
		others = append(others, mirrorUse{path: path, used: fi.ModTime(), size: size})
		total += size
	}
	sort.Slice(others, func(i, j int) bool { return others[i].used.Before(others[j].used) })
	count := len(others) + 1
	for _, m := range others {
		overCount := opts.maxMirrors > 0 && count > opts.maxMirrors
		overBytes := opts.maxMirrorBytes > 0 && total > opts.maxMirrorBytes
		if !overCount && !overBytes {
			break
		}
		if directEvict(m.path, git) {
			count--
			total -= m.size
		}
	}
	return nil
}

// directEvict removes mirror unless a checkout holds its lock or a run's
// worktree still points into it. The lock file stays: unlinking it while
// another checkout waits on it would let a third lock a fresh file beside it.
func directEvict(mirror string, git func(...string) (string, error)) bool {
	lock, err := fssecure.OpenFile(mirror+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return false
	}
	defer func() { _ = lock.Close() }()
	if ok, err := cacheLock(lock, cacheLockExclusiveNonblock); err != nil || !ok {
		return false
	}
	if _, err := git("-C", mirror, "worktree", "prune"); err != nil {
		return false
	}
	worktrees, err := os.ReadDir(filepath.Join(mirror, "worktrees"))
	if (err != nil && !os.IsNotExist(err)) || len(worktrees) > 0 {
		return false
	}
	return os.RemoveAll(mirror) == nil
}

func directDirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
