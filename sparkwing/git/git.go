package git

import (
	"context"
	"errors"

	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

// CloneOption configures a clone.
type (
	CloneOption func(*cloneConfig)
	cloneConfig struct{ depth int }
)

// WithDepth limits the fetched history to n commits.
func WithDepth(n int) CloneOption { return func(c *cloneConfig) { c.depth = n } }

// Clone acquires url directly into a new destination directory.
func Clone(ctx context.Context, url, destDir string, opts ...CloneOption) error {
	cfg := cloneConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return sdkserve.Helper(ctx, "git.Clone", []any{url, destDir, cfg.depth}, nil)
}

// Fetch updates the configured origin directly.
func Fetch(ctx context.Context, repoDir string) error {
	return sdkserve.Helper(ctx, "git.Fetch", []any{repoDir}, nil)
}

// ErrTagAlreadyExists is returned by PushTag when the tag already
// exists on the remote. Go-module safety: never force-push a tag.
// Callers wanting to move a tag must delete it on the remote first.
var ErrTagAlreadyExists = errors.New("git: tag already exists on remote")

// ShortCommit returns the HEAD commit SHA in repoDir truncated to 12
// characters. Errors when repoDir is not a git working tree.
func ShortCommit(ctx context.Context, repoDir string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.ShortCommit", []any{repoDir}, &result)
	return result, err
}

// CurrentSHA returns the full HEAD commit SHA in repoDir. Errors
// loudly when not in a git repo.
func CurrentSHA(ctx context.Context, repoDir string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.CurrentSHA", []any{repoDir}, &result)
	return result, err
}

// CurrentBranch returns the branch name HEAD points at in repoDir.
// Detached HEAD returns ("", nil) -- a normal state during CI builds
// that check out a specific SHA. Errors loudly when not in a git
// repo.
func CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.CurrentBranch", []any{repoDir}, &result)
	return result, err
}

// DefaultBranch returns the repo's default branch name (typically
// "main" or "master"). Reads it from origin's HEAD symbolic ref --
// the same source `git remote show origin` uses. Returns ("", nil)
// when no origin remote exists or origin's HEAD isn't published; the
// caller treats that as "no default branch known" rather than an
// error so guards stay safe-by-default.
func DefaultBranch(ctx context.Context, repoDir string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.DefaultBranch", []any{repoDir}, &result)
	return result, err
}

// RemoteOriginURL returns the URL of `origin` in repoDir, or "" with
// nil error when no origin remote is configured. Errors only when
// repoDir isn't a git tree.
func RemoteOriginURL(ctx context.Context, repoDir string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.RemoteOriginURL", []any{repoDir}, &result)
	return result, err
}

// IsDirty reports whether the working tree in repoDir has uncommitted
// changes -- either unstaged or staged-but-not-committed. Errors
// loudly when not in a git repo.
func IsDirty(ctx context.Context, repoDir string) (bool, error) {
	var result bool
	err := sdkserve.Helper(ctx, "git.IsDirty", []any{repoDir}, &result)
	return result, err
}

// FilesetHash returns a deterministic 12-char hex hash derived from
// the contents of every file that would land in a Docker build
// context for repoDir. With git, hashes tracked + untracked-not-
// ignored files; without, falls back to a filesystem walk with a
// small skip list. .dockerignore is honored in both modes.
//
// Paths (including whitespace and non-ASCII names), file permissions, and contents
// contribute to the hash. Unreadable inputs or ignore rules return an error;
// tracked files absent from disk are omitted.
// File contents are read from disk (not from the git blob) so
// staged-but-not-committed changes are reflected in the hash --
// content addressing keys off the tree the build will actually see.
func FilesetHash(ctx context.Context, repoDir string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.FilesetHash", []any{repoDir}, &result)
	return result, err
}

// ChangedFiles returns paths (repo-relative) modified between `since`
// and HEAD in repoDir. `since` may be any git revision (commit SHA,
// branch name, tag, "HEAD~5"). Empty `since` returns the working
// tree's currently-modified set (`git diff --name-only HEAD`).
func ChangedFiles(ctx context.Context, repoDir, since string) ([]string, error) {
	var result []string
	err := sdkserve.Helper(ctx, "git.ChangedFiles", []any{repoDir, since}, &result)
	return result, err
}

// TagsAtHead returns every tag pointing at HEAD in repoDir. Order is
// whatever git returns (lexical by default).
func TagsAtHead(ctx context.Context, repoDir string) ([]string, error) {
	var result []string
	err := sdkserve.Helper(ctx, "git.TagsAtHead", []any{repoDir}, &result)
	return result, err
}

// LatestTag returns the highest semver tag in repoDir matching the
// given prefix, or "" if none. Ordering is by semver: v0.10.0 >
// v0.2.0. Non-semver tags are skipped. Prefix is matched literally
// and preserved in the returned tag; pass "" to consider every tag.
func LatestTag(ctx context.Context, repoDir, prefix string) (string, error) {
	var result string
	err := sdkserve.Helper(ctx, "git.LatestTag", []any{repoDir, prefix}, &result)
	return result, err
}

// TagExistsOnRemote checks whether `tag` exists on origin in repoDir.
// Returns (true, nil) if present, (false, nil) if absent, (_, err)
// on transport failure.
func TagExistsOnRemote(ctx context.Context, repoDir, tag string) (bool, error) {
	var result bool
	err := sdkserve.Helper(ctx, "git.TagExistsOnRemote", []any{repoDir, tag}, &result)
	return result, err
}

// PushTag creates an annotated tag locally in repoDir and pushes it
// to origin. Refuses with ErrTagAlreadyExists if the remote already
// has that tag or a local tag points elsewhere. A local tag at HEAD can
// be retried after a failed push. Tags are never force-pushed.
func PushTag(ctx context.Context, repoDir, tag, message string) error {
	return sdkserve.Helper(ctx, "git.PushTag", []any{repoDir, tag, message}, nil)
}
