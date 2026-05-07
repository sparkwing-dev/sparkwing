package docker

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/v2/sparkwing/git"
	"github.com/sparkwing-dev/sparkwing/v2/sparkwing/planguard"
)

// ImageTag holds the deterministic image-tag components for a single
// build: short commit SHA, content hash of the build's fileset, and a
// dirty bit. The same tag format is consumed by every deploy path
// (kind, gitops, ECR) so a tag's shape alone identifies the build
// inputs.
//
// Branch is a separate readable label; it isn't part of DeployTag /
// ProdTag (those stay content-addressed).
type ImageTag struct {
	Commit  string
	Content string
	Branch  string
	Dirty   bool
}

// DeployTag is the canonical content-addressed tag applied to every
// build. Format: commit-<sha>-files-<hash>[-dirty]. Two trees with
// identical content produce identical tags even on different commits;
// a clean commit always produces the same tag across re-runs.
func (t ImageTag) DeployTag() string {
	tag := "commit-" + t.Commit + "-files-" + t.Content
	if t.Dirty {
		tag += "-dirty"
	}
	return tag
}

// ProdTag is the gitops-consumed tag: DeployTag with a "-prod"
// suffix, so kind and prod gitops flows don't collide on the same
// digest in image-hash bookkeeping.
func (t ImageTag) ProdTag() string {
	return t.DeployTag() + "-prod"
}

// All returns every tag a build pipeline should apply: DeployTag plus
// ProdTag. Pushing both lets kind callers resolve by DeployTag and
// prod gitops resolve by ProdTag without re-tagging at deploy time.
func (t ImageTag) All() []string {
	return []string{t.DeployTag(), t.ProdTag()}
}

// ComputeTags reads the git repo state at the process CWD and
// returns an ImageTag describing it. Not running inside a git repo
// is a hard error. Pipelines that operate on a sibling clone use
// ComputeTagsIn instead.
func ComputeTags(ctx context.Context) (ImageTag, error) {
	planguard.Guard(ctx, "docker.ComputeTags")
	return ComputeTagsIn(ctx, "")
}

// ComputeTagsIn reads the git repo state in repoDir and returns an
// ImageTag describing it. Empty repoDir falls back to process CWD.
func ComputeTagsIn(ctx context.Context, repoDir string) (ImageTag, error) {
	planguard.Guard(ctx, "docker.ComputeTagsIn")
	commit, err := git.ShortCommit(ctx, repoDir)
	if err != nil {
		return ImageTag{}, fmt.Errorf("docker: ComputeTags commit: %w", err)
	}
	branch, err := git.CurrentBranch(ctx, repoDir)
	if err != nil {
		return ImageTag{}, fmt.Errorf("docker: ComputeTags branch: %w", err)
	}
	dirty, err := git.IsDirty(ctx, repoDir)
	if err != nil {
		return ImageTag{}, fmt.Errorf("docker: ComputeTags dirty: %w", err)
	}
	content, err := git.FilesetHash(ctx, repoDir)
	if err != nil {
		return ImageTag{}, fmt.Errorf("docker: ComputeTags fileset: %w", err)
	}
	return ImageTag{
		Commit:  commit,
		Content: content,
		Branch:  branch,
		Dirty:   dirty,
	}, nil
}
