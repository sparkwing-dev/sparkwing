package sparkwing

import (
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing/git"
)

// Git is the run-scoped view of a single git working tree. The same
// instance lives on both `RunContext.Git` (passed to Plan and Job)
// and `Runtime().Git` (read by SDK helpers like docker.ComputeTags).
//
// Data fields (SHA, Branch, Repo, RepoURL) are populated by the
// orchestrator at run start. Methods (IsDirty, FilesetHash, …) shell
// out fresh each call against `workDir`, which is left unexported so
// callers can't spoof "I'm running in repo X" without going through
// NewGit / NewGitFromTree.
type Git struct {
	SHA     string `json:"sha,omitempty"`      // full 40-char commit
	Branch  string `json:"branch,omitempty"`   // "main", "" when detached
	Repo    string `json:"repo,omitempty"`     // "owner/name"
	RepoURL string `json:"repo_url,omitempty"` // "git@github.com:owner/name.git"

	// workDir is the absolute path to the working tree. Unexported so
	// the only legitimate constructors are NewGit / NewGitFromTree;
	// cross-repo ops go through sparkwing/git directly.
	workDir string `json:"-"`
}

// NewGit constructs a Git with the supplied data fields and workDir.
// Used by the orchestrator at dispatch time.
func NewGit(workDir, sha, branch, repo, repoURL string) *Git {
	return &Git{
		SHA:     sha,
		Branch:  branch,
		Repo:    repo,
		RepoURL: repoURL,
		workDir: workDir,
	}
}

// NewGitFromTree builds a Git by shelling out to inspect workDir.
// Returns a Git with data fields populated and an unset Repo when
// origin isn't a github URL. Empty workDir means "use the process
// CWD".
func NewGitFromTree(ctx context.Context, workDir string) (*Git, error) {
	g := &Git{workDir: workDir}
	sha, err := git.CurrentSHA(ctx, workDir)
	if err != nil {
		return nil, err
	}
	g.SHA = sha
	branch, err := git.CurrentBranch(ctx, workDir)
	if err != nil {
		return nil, err
	}
	g.Branch = branch
	repoURL, err := git.RemoteOriginURL(ctx, workDir)
	if err != nil {
		return nil, err
	}
	g.RepoURL = repoURL
	g.Repo = repoSlugFromURL(repoURL)
	return g, nil
}

// WorkDir returns the absolute path of the working tree this Git
// describes, or "" when constructed without a real clone.
func (g *Git) WorkDir() string {
	if g == nil {
		return ""
	}
	return g.workDir
}

// ShortSHA returns g.SHA truncated to 12 chars; safe to call on a
// nil receiver (returns "").
func (g *Git) ShortSHA() string {
	if g == nil || g.SHA == "" {
		return ""
	}
	if len(g.SHA) > 12 {
		return g.SHA[:12]
	}
	return g.SHA
}

// Name returns the bare repo name (the part after the last "/" in
// Repo). Empty when Repo is unset. Use Repo for the unambiguous
// "owner/name" slug.
func (g *Git) Name() string {
	if g == nil || g.Repo == "" {
		return ""
	}
	if i := strings.LastIndex(g.Repo, "/"); i >= 0 {
		return g.Repo[i+1:]
	}
	return g.Repo
}

// IsDirty reports whether the working tree has uncommitted changes.
// Errors when not in a git repo (no env fallback).
func (g *Git) IsDirty(ctx context.Context) (bool, error) {
	return git.IsDirty(ctx, g.dir())
}

// FilesetHash returns a deterministic hash of every tracked file's
// content. Untracked files don't contribute.
func (g *Git) FilesetHash(ctx context.Context) (string, error) {
	return git.FilesetHash(ctx, g.dir())
}

// ChangedFiles returns repo-relative paths modified between `since`
// and HEAD. `since` is any git revision; empty falls back to the
// working tree's currently-modified set.
func (g *Git) ChangedFiles(ctx context.Context, since string) ([]string, error) {
	return git.ChangedFiles(ctx, g.dir(), since)
}

// TagsAtHead returns every tag pointing at HEAD.
func (g *Git) TagsAtHead(ctx context.Context) ([]string, error) {
	return git.TagsAtHead(ctx, g.dir())
}

// LatestTag returns the highest semver tag with prefix.
func (g *Git) LatestTag(ctx context.Context, prefix string) (string, error) {
	return git.LatestTag(ctx, g.dir(), prefix)
}

// PushTag creates the annotated tag locally and pushes it to origin.
// Refuses to overwrite an existing tag (ErrTagAlreadyExists).
func (g *Git) PushTag(ctx context.Context, tag, message string) error {
	return git.PushTag(ctx, g.dir(), tag, message)
}

// dir returns the working dir to shell into; empty falls back to
// process CWD via the underlying git package.
func (g *Git) dir() string {
	if g == nil {
		return ""
	}
	return g.workDir
}

// GithubOwnerRepo splits a "owner/name" slug into its parts. Returns
// ("", "") when slug isn't in the expected form.
func GithubOwnerRepo(slug string) (owner, repo string) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", ""
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}

// repoSlugFromURL parses "owner/name" from a github SSH or HTTPS URL.
// Returns "" for non-github URLs.
func repoSlugFromURL(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return ""
	}
	repoURL = strings.TrimSuffix(repoURL, ".git")
	rest := repoURL
	switch {
	case strings.HasPrefix(rest, "git@github.com:"):
		rest = strings.TrimPrefix(rest, "git@github.com:")
	case strings.HasPrefix(rest, "https://github.com/"):
		rest = strings.TrimPrefix(rest, "https://github.com/")
	case strings.HasPrefix(rest, "http://github.com/"):
		rest = strings.TrimPrefix(rest, "http://github.com/")
	default:
		return ""
	}
	rest = strings.Trim(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
