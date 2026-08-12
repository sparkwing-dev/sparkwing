<!-- GENERATED from the `sparkwing` package via go/doc (internal/sdkref). Do not edit by hand; regenerate with `bash bin/gen-sdk-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# SDK API reference: `sparkwing/git`

Package git is the sparkwing SDK's repo-inspection helper layer: commit SHA, branch, dirty-tree detection, deterministic fileset hash, tag listing, and safe tag push.

Import as `swgit "github.com/sparkwing-dev/sparkwing/sparkwing/git"`. The root package and the other subpackages are indexed in [sdk-reference.md](sdk-reference.md).

## Functions

- `func ChangedFiles(ctx context.Context, repoDir, since string) ([]string, error)` -- ChangedFiles returns paths (repo-relative) modified between `since` and HEAD in repoDir.
- `func Clone(ctx context.Context, url, destDir string, opts ...CloneOption) error` -- Clone clones url into destDir.
- `func CurrentBranch(ctx context.Context, repoDir string) (string, error)` -- CurrentBranch returns the branch name HEAD points at in repoDir.
- `func CurrentSHA(ctx context.Context, repoDir string) (string, error)` -- CurrentSHA returns the full HEAD commit SHA in repoDir.
- `func DefaultBranch(ctx context.Context, repoDir string) (string, error)` -- DefaultBranch returns the repo's default branch name (typically "main" or "master").
- `func Fetch(ctx context.Context, repoDir string) error` -- Fetch runs `git fetch` in repoDir.
- `func FilesetHash(ctx context.Context, repoDir string) (string, error)` -- FilesetHash returns a deterministic 12-char hex hash derived from the contents of every file that would land in a Docker build context for repoDir.
- `func IsDirty(ctx context.Context, repoDir string) (bool, error)` -- IsDirty reports whether the working tree in repoDir has uncommitted changes -- either unstaged or staged-but-not-committed.
- `func LatestTag(ctx context.Context, repoDir, prefix string) (string, error)` -- LatestTag returns the highest semver tag in repoDir matching the given prefix, or "" if none.
- `func PushTag(ctx context.Context, repoDir, tag, message string) error` -- PushTag creates an annotated tag locally in repoDir and pushes it to origin.
- `func RemoteOriginURL(ctx context.Context, repoDir string) (string, error)` -- RemoteOriginURL returns the URL of `origin` in repoDir, or "" with nil error when no origin remote is configured.
- `func ShortCommit(ctx context.Context, repoDir string) (string, error)` -- ShortCommit returns the HEAD commit SHA in repoDir truncated to 12 characters.
- `func TagExistsOnRemote(ctx context.Context, repoDir, tag string) (bool, error)` -- TagExistsOnRemote checks whether `tag` exists on origin in repoDir.
- `func TagsAtHead(ctx context.Context, repoDir string) ([]string, error)` -- TagsAtHead returns every tag pointing at HEAD in repoDir.

## Types

### type CloneOption

CloneOption configures optional git-clone behavior.

```
type CloneOption func(*cloneConfig)
```

- `func WithDepth(n int) CloneOption` -- WithDepth limits the clone to the most recent n commits (--depth n).

## Variables

```
var ErrTagAlreadyExists = errors.New("git: tag already exists on remote")
```
