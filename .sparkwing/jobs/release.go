package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// ReleaseArgs is the typed CLI surface for the public-sparkwing
// release pipeline. --version is optional: when omitted the
// pipeline bumps --bump (default minor) off the latest origin tag.
//
// Preview / no-mutation mode is delivered through sparkwing's reserved
// `--dry-run` flag; each step below either marks itself
// SafeWithoutDryRun (read-only checks) or provides a .DryRun(...)
// body (the tag push), so the pipeline doesn't carry its own flag.
type ReleaseArgs struct {
	Version string `flag:"version" desc:"Explicit release version (e.g. v1.5.5). When empty, derived from latest origin tag + --bump."`
	Bump    string `flag:"bump" desc:"Auto-bump kind when --version is empty: patch|minor|major. Default: minor"`
}

// Release tags and pushes a new public-sparkwing version. The tag
// push triggers `.github/workflows/release.yaml`, which builds
// cross-platform binaries (for GitHub Releases) and multi-arch
// container images (for GHCR). This pipeline does NOT duplicate
// that work -- its job is to validate the release shape (clean
// tree, free tag, non-empty CHANGELOG [Unreleased] section) and
// push a single tag, then step out of the way.
//
// The "never force-push a Go module tag" invariant from the
// platform-repo release pipeline applies here too: validate-version
// hard-refuses a tag that already exists on origin.
type Release struct {
	sparkwing.Base
	args ReleaseArgs
}

func (Release) ShortHelp() string {
	return "Tag and push a public sparkwing version (kicks GH-Actions release)"
}

func (Release) Help() string {
	return "Validates the release shape (clean tree, free tag, non-empty CHANGELOG.md [Unreleased] section) and pushes a vX.Y.Z tag to origin. The .github/workflows/release.yaml workflow takes over from the tag push to build cross-platform binaries (uploaded to GH Releases) and multi-arch container images (published to GHCR). This pipeline never builds or publishes artifacts itself."
}

func (Release) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Auto-pick version by bumping latest origin tag", Command: "sparkwing run release"},
		{Comment: "Tag and push an explicit version", Command: "sparkwing run release --version v1.5.5"},
		{Comment: "Preview without pushing", Command: "sparkwing run release --dry-run"},
	}
}

func (r *Release) Plan(_ context.Context, plan *sparkwing.Plan, in ReleaseArgs, _ sparkwing.RunContext) error {
	r.args = in

	repoDir, err := repoRoot()
	if err != nil {
		return fmt.Errorf("release: locate repo root: %w", err)
	}

	// All git/changelog probing happens inside Jobs. The
	// version-resolve probe is small enough to inline; the rest are
	// regular nodes so retries work cleanly on a transient origin.
	discover := sparkwing.Job(plan, "discover-version", &resolveVersionJob{
		Explicit: r.args.Version,
		Bump:     r.args.Bump,
		RepoDir:  repoDir,
	}).Inline()
	versionRef := sparkwing.RefTo[string](discover)

	validate := sparkwing.Job(plan, "validate-version", &validateVersionJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	validate.Needs(discover)

	clean := sparkwing.Job(plan, "check-clean-tree", &checkCleanTreeJob{
		RepoDir: repoDir,
	})

	changelog := sparkwing.Job(plan, "prepare-changelog", &prepareChangelogJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	changelog.Needs(discover, clean)

	pushTag := sparkwing.Job(plan, "push-tag", &pushTagJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	pushTag.Needs(validate, clean, changelog)
	return nil
}

// repoRoot returns the working directory of the sparkwing run. In the
// public sparkwing repo `.sparkwing/` lives at the module root, so
// the SDK's WorkDir() is the right answer.
func repoRoot() (string, error) {
	d := sparkwing.WorkDir()
	if d == "" {
		return "", errors.New("sparkwing.WorkDir() returned empty")
	}
	if _, err := os.Stat(filepath.Join(d, ".git")); err != nil {
		return "", fmt.Errorf("not a git repo at %s: %w", d, err)
	}
	return d, nil
}

// resolveVersionJob picks the version to release, in order:
//  1. explicit --version  -> use it.
//  2. tag + bump          -> bump kind off the highest origin tag.
//
// First-ever release (no tags) yields v0.1.0 from the bump fallback.
// CHANGELOG.md is maintained separately (see VERSIONING.md); the
// check-changelog node verifies the [Unreleased] section has at
// least one entry before push-tag runs. The GH-Actions release
// workflow additionally emits commit-walk release notes via
// `gh release create --generate-notes` for the GitHub Release page.
type resolveVersionJob struct {
	sparkwing.Base
	sparkwing.Produces[string]

	Explicit string
	Bump     string
	RepoDir  string
}

func (j *resolveVersionJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "run", j.run).SafeWithoutDryRun(), nil
}

func (j *resolveVersionJob) run(ctx context.Context) (string, error) {
	if s := strings.TrimSpace(j.Explicit); s != "" {
		if err := validateReleaseVersion(s); err != nil {
			return "", err
		}
		sparkwing.Info(ctx, "using explicit version: %s", s)
		return s, nil
	}
	bump := strings.TrimSpace(j.Bump)
	if bump == "" {
		bump = "minor"
	}
	switch bump {
	case "patch", "minor", "major":
	default:
		return "", fmt.Errorf("release: --bump must be patch|minor|major (got %q)", bump)
	}
	latest, err := latestSemverTagIn(ctx, j.RepoDir)
	if err != nil {
		return "", fmt.Errorf("release: resolve latest tag: %w", err)
	}
	if latest == "" {
		sparkwing.Info(ctx, "no existing tag; defaulting to v0.1.0")
		return "v0.1.0", nil
	}
	next, err := bumpVersion(latest, bump)
	if err != nil {
		return "", fmt.Errorf("release: bump %s: %w", latest, err)
	}
	if err := validateReleaseVersion(next); err != nil {
		return "", err
	}
	sparkwing.Info(ctx, "bumped %s -> %s (%s)", latest, next, bump)
	return next, nil
}

// validateVersionJob parses the resolved version string as semver
// and fails loudly if the tag already exists on origin. Hard refusal
// gate for "never force-push a module tag" -- the GH-Actions release
// workflow assumes the tag push is the source of truth.
type validateVersionJob struct {
	sparkwing.Base
	Version sparkwing.Ref[string]
	RepoDir string
}

func (j *validateVersionJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
	return nil, nil
}

func (j *validateVersionJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	if err := validateReleaseVersion(version); err != nil {
		return err
	}
	exists, err := tagExistsOnRemote(ctx, j.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: check remote tags: %w", err)
	}
	if exists {
		return fmt.Errorf("release: tag %s already exists on origin (never force-push a module tag; increment to a new version)", version)
	}
	sparkwing.Info(ctx, "version %s is free on origin", version)
	return nil
}

// checkCleanTreeJob refuses to proceed if the working tree has
// uncommitted changes. Tag pushes always come from a clean HEAD; a
// dirty tree usually means the operator forgot to commit.
type checkCleanTreeJob struct {
	sparkwing.Base
	RepoDir string
}

func (j *checkCleanTreeJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
	return nil, nil
}

func (j *checkCleanTreeJob) run(ctx context.Context) error {
	out, err := runGitIn(ctx, j.RepoDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("release: git status: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("release: working tree is dirty:\n%s\ncommit or stash before releasing", strings.TrimSpace(out))
	}
	sparkwing.Info(ctx, "working tree is clean")
	return nil
}

// prepareChangelogJob validates CHANGELOG.md has shippable content
// for this release and renames the `## [Unreleased]` section to
// `## [vX.Y.Z] - YYYY-MM-DD`, leaving a fresh empty `## [Unreleased]`
// heading above. Commits the rewrite so the tag points at a commit
// with the [vX.Y.Z] section in place -- the GH-Actions release
// workflow extracts that section verbatim as the GitHub Release body
// (see .github/workflows/release.yaml + bin/extract-changelog-section.sh).
//
// Idempotent: if `[vX.Y.Z]` already exists in the file, the rewrite
// is a no-op (an operator who re-runs after a tag-push failure
// shouldn't get a duplicate commit). Validation still runs in that
// case to confirm the existing section has content.
//
// Refuses to run when:
//   - [Unreleased] has no bullet entries AND [vX.Y.Z] doesn't exist
//     (nothing to ship)
//   - BOTH [Unreleased] and [vX.Y.Z] have content (ambiguous: the
//     operator probably split the entries by hand and needs to
//     consolidate before re-running)
//
// The PR-time CI gate (bin/check-changelog.sh in `sparkwing run
// lint`) already enforces non-empty [Unreleased] on covered-surface
// changes; this is the defense-in-depth fence at release time.
type prepareChangelogJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (j *prepareChangelogJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).DryRun(j.dryRun)
	return nil, nil
}

func (j *prepareChangelogJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	path := filepath.Join(j.RepoDir, "CHANGELOG.md")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	action, err := planChangelogRewrite(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	switch action.kind {
	case rewriteNoop:
		sparkwing.Info(ctx, "CHANGELOG.md already has [%s] section (%d entries); skipping rewrite", version, action.versionEntries)
		return nil
	case rewriteApply:
		sparkwing.Info(ctx, "renaming CHANGELOG.md [Unreleased] -> [%s] (%d entries)", version, action.unreleasedEntries)
	}
	if err := os.WriteFile(path, []byte(action.newBody), 0o644); err != nil {
		return fmt.Errorf("release: write CHANGELOG.md: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "add", "CHANGELOG.md"); err != nil {
		return fmt.Errorf("release: git add CHANGELOG.md: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "commit", "-m", "release: "+version+" changelog"); err != nil {
		return fmt.Errorf("release: git commit CHANGELOG.md: %w", err)
	}
	sparkwing.Info(ctx, "committed CHANGELOG.md rewrite for %s", version)
	return nil
}

func (j *prepareChangelogJob) dryRun(ctx context.Context) error {
	version := j.Version.Get(ctx)
	path := filepath.Join(j.RepoDir, "CHANGELOG.md")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	action, err := planChangelogRewrite(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	switch action.kind {
	case rewriteNoop:
		sparkwing.Info(ctx, "dry-run: CHANGELOG.md already has [%s] (%d entries); rewrite would be a no-op", version, action.versionEntries)
	case rewriteApply:
		sparkwing.Info(ctx, "dry-run: would rename [Unreleased] -> [%s] (%d entries) and commit", version, action.unreleasedEntries)
	}
	return nil
}

// changelogRewriteKind distinguishes "operator already prepared the
// CHANGELOG" from "we need to apply the rewrite ourselves".
type changelogRewriteKind int

const (
	rewriteApply changelogRewriteKind = iota
	rewriteNoop
)

type changelogRewrite struct {
	kind              changelogRewriteKind
	newBody           string
	unreleasedEntries int
	versionEntries    int
}

// planChangelogRewrite decides what (if anything) prepareChangelogJob
// should do to body for the given version. Pure function so the test
// suite can exercise every branch without touching git or the
// filesystem.
func planChangelogRewrite(body, version string) (changelogRewrite, error) {
	unreleased, err := unreleasedEntries(body)
	if err != nil {
		return changelogRewrite{}, fmt.Errorf("parse CHANGELOG.md: %w", err)
	}
	versionCount, err := versionEntries(body, version)
	if err != nil {
		return changelogRewrite{}, fmt.Errorf("parse CHANGELOG.md: %w", err)
	}
	switch {
	case versionCount > 0 && unreleased == 0:
		return changelogRewrite{kind: rewriteNoop, versionEntries: versionCount}, nil
	case versionCount > 0 && unreleased > 0:
		return changelogRewrite{}, fmt.Errorf(
			"CHANGELOG.md has BOTH [Unreleased] (%d entries) and [%s] (%d entries) populated -- "+
				"consolidate the entries under one section before re-running",
			unreleased, version, versionCount,
		)
	case unreleased == 0:
		return changelogRewrite{}, fmt.Errorf(
			"CHANGELOG.md [Unreleased] is empty -- no entries to ship as %s. "+
				"Add at least one entry under Added/Changed/Fixed/Removed/Security before re-running release",
			version,
		)
	}
	newBody, err := rewriteUnreleasedToVersion(body, version, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return changelogRewrite{}, err
	}
	return changelogRewrite{
		kind:              rewriteApply,
		newBody:           newBody,
		unreleasedEntries: unreleased,
	}, nil
}

// rewriteUnreleasedToVersion replaces the first `## [Unreleased]` /
// `## Unreleased` heading with a pair of headings: a fresh empty
// `## [Unreleased]` followed by `## [vX.Y.Z] - YYYY-MM-DD`. Returns
// an error if no [Unreleased] heading is found.
func rewriteUnreleasedToVersion(body, version, date string) (string, error) {
	re := regexp.MustCompile(`(?m)^## \[?Unreleased\]?\s*$`)
	loc := re.FindStringIndex(body)
	if loc == nil {
		return "", fmt.Errorf("CHANGELOG.md has no [Unreleased] heading to rewrite")
	}
	newHeader := "## [Unreleased]\n\n## [" + version + "] - " + date
	return body[:loc[0]] + newHeader + body[loc[1]:], nil
}

// versionEntries counts the bullets under `## [vX.Y.Z]` (with or
// without a date suffix). Mirrors unreleasedEntries' parsing.
func versionEntries(body, version string) (int, error) {
	target := strings.TrimSpace(version)
	if target == "" {
		return 0, fmt.Errorf("empty version")
	}
	lines := strings.Split(body, "\n")
	in := false
	count := 0
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "## ") {
			rest := strings.TrimPrefix(line, "## ")
			// Tolerate `## [v1.2.3]` and `## [v1.2.3] - 2026-05-20`
			// and bare `## v1.2.3` forms.
			rest = strings.TrimSpace(rest)
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
			// Strip any trailing ` - date` after the version label.
			if i := strings.Index(rest, "] - "); i >= 0 {
				rest = rest[:i]
			}
			if dash := strings.Index(rest, " - "); dash >= 0 {
				rest = rest[:dash]
			}
			if strings.EqualFold(strings.TrimSpace(rest), target) {
				in = true
				continue
			}
			if in {
				break
			}
			continue
		}
		if !in {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			count++
		}
	}
	return count, nil
}

// unreleasedEntries counts the bullet lines (lines starting with
// `- `) inside the `## [Unreleased]` (or `## Unreleased`) section of
// a Keep-a-Changelog-formatted body. Stops at the next top-level
// `## ` heading or EOF. Returns 0 if the section is missing or
// contains only sub-headings / blank lines; returns an error only
// when the body is unreadable.
func unreleasedEntries(body string) (int, error) {
	lines := strings.Split(body, "\n")
	in := false
	count := 0
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "## ") {
			h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			h = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"))
			if strings.EqualFold(h, "Unreleased") {
				in = true
				continue
			}
			if in {
				break
			}
			continue
		}
		if !in {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			count++
		}
	}
	return count, nil
}

// pushTagJob creates the annotated tag and pushes it to origin. The
// GH-Actions release workflow takes over from this push. Under
// `--dry-run`, the dryRun body runs in place of run -- it logs the
// planned tag without mutating local refs or origin.
type pushTagJob struct {
	sparkwing.Base
	Version sparkwing.Ref[string]
	RepoDir string
}

func (j *pushTagJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).
		DryRun(j.dryRun).
		Risk("destructive", "prod")
	return nil, nil
}

func (j *pushTagJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	exists, err := tagExistsOnRemote(ctx, j.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: re-check remote tags: %w", err)
	}
	if exists {
		return fmt.Errorf("release: tag %s appeared on origin between validate and push (race); abort", version)
	}
	branch, err := currentBranch(ctx, j.RepoDir)
	if err != nil {
		return fmt.Errorf("release: detect current branch: %w", err)
	}
	if branch != "main" {
		return fmt.Errorf("release: refusing to push from branch %q -- release pipeline expects to run on main "+
			"so the changelog-rewrite commit and the tag land on the default branch", branch)
	}
	// Push the branch first so origin has the CHANGELOG-rewrite commit
	// the tag points at. Then the tag. Go modules and the GH-Actions
	// release workflow both assume the tagged commit is reachable from
	// a branch ref on origin.
	if _, err := runGitIn(ctx, j.RepoDir, "push", "origin", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("release: push branch: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "tag", "-a", version, "-m", "Release "+version); err != nil {
		return fmt.Errorf("release: create tag: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "push", "origin", "refs/tags/"+version); err != nil {
		return fmt.Errorf("release: push tag: %w", err)
	}
	sparkwing.Info(ctx, "pushed %s + branch %s to origin (GH-Actions release.yaml will take over)", version, branch)
	return nil
}

func (j *pushTagJob) dryRun(ctx context.Context) error {
	version := j.Version.Get(ctx)
	branch, err := currentBranch(ctx, j.RepoDir)
	if err != nil {
		sparkwing.Info(ctx, "dry-run: would tag %s and push branch+tag to origin (current-branch lookup failed: %v)", version, err)
		return nil
	}
	sparkwing.Info(ctx, "dry-run: would push branch %s + tag %s to origin", branch, version)
	return nil
}

// currentBranch returns the abbreviated ref name (e.g. "main") of
// HEAD. Detached HEAD returns "HEAD" -- the caller refuses the
// release in that case via the `!= "main"` check.
func currentBranch(ctx context.Context, repoDir string) (string, error) {
	out, err := runGitIn(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// --- helpers (kept local; cross-module helper imports don't work
// for the .sparkwing/ tree since it's a separate Go module). ---

func validateReleaseVersion(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("release: --version is required (e.g. --version v0.6.1)")
	}
	if !strings.HasPrefix(v, "v") {
		return fmt.Errorf("release: version %q must begin with 'v' (e.g. v0.6.1)", v)
	}
	if !semver.IsValid(v) {
		return fmt.Errorf("release: version %q is not valid semver (expected vX.Y.Z)", v)
	}
	if semver.Prerelease(v) != "" || semver.Build(v) != "" {
		return fmt.Errorf("release: version %q includes pre-release / build metadata; release pipeline only cuts stable tags", v)
	}
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return fmt.Errorf("release: version %q must be vX.Y.Z", v)
	}
	// Pre-1.0 lock. Every breaking change is permitted in minor
	// bumps while we're on v0.x (see VERSIONING.md). Stepping to
	// v1.0.0+ commits the public API surface and switches the
	// deprecation contract -- that decision must be a deliberate
	// code change, not a typo or `--bump major` accident. To unlock:
	// remove this branch.
	if semver.Major(v) != "v0" {
		return fmt.Errorf("release: version %q is v1.0.0+ but sparkwing is locked to v0.x. "+
			"Bumping to v1+ commits the public API surface (see VERSIONING.md); "+
			"if that's intentional, remove the pre-1.0 lock in .sparkwing/jobs/release.go and resubmit", v)
	}
	return nil
}

func tagExistsOnRemote(ctx context.Context, repoDir, tag string) (bool, error) {
	out, err := runGitIn(ctx, repoDir, "ls-remote", "--tags", "origin", "refs/tags/"+tag)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func runGitIn(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := sparkwing.Exec(ctx, "git", args...).Dir(dir).Run()
	if err != nil {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return res.Stdout, nil
}

// latestSemverTagIn returns the highest stable-semver tag visible on
// origin. Reads via `ls-remote --tags` rather than `git tag --list` so
// stale local tags (orphans from before an OSS scrub, force-deleted
// upstream refs, etc.) can't bias the bump fallback. The release
// pipeline's "what's the next version" decision is fundamentally a
// statement about what the world has seen -- not what this checkout
// happens to remember.
func latestSemverTagIn(ctx context.Context, repoDir string) (string, error) {
	out, err := runGitIn(ctx, repoDir, "ls-remote", "--tags", "origin")
	if err != nil {
		return "", err
	}
	var best string
	for _, line := range strings.Split(out, "\n") {
		// Each line: "<sha>\trefs/tags/<tag>" or
		// "<sha>\trefs/tags/<tag>^{}" for the dereferenced peel of an
		// annotated tag. The peeled entry duplicates the name; either
		// form works for our compare so we just strip both suffixes.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		const prefix = "refs/tags/"
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		t := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), "^{}")
		if !semver.IsValid(t) {
			continue
		}
		if semver.Prerelease(t) != "" || semver.Build(t) != "" {
			continue
		}
		if best == "" || semver.Compare(t, best) > 0 {
			best = t
		}
	}
	return best, nil
}

func bumpVersion(v, kind string) (string, error) {
	if !semver.IsValid(v) {
		return "", fmt.Errorf("not semver: %s", v)
	}
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not vX.Y.Z: %s", v)
	}
	var major, minor, patch int
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return "", err
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minor); err != nil {
		return "", err
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &patch); err != nil {
		return "", err
	}
	switch kind {
	case "major":
		major++
		minor = 0
		patch = 0
	case "minor":
		minor++
		patch = 0
	case "patch":
		patch++
	default:
		return "", fmt.Errorf("bump kind %q not in patch|minor|major", kind)
	}
	return fmt.Sprintf("v%d.%d.%d", major, minor, patch), nil
}

func init() {
	sparkwing.Register[ReleaseArgs]("release", func() sparkwing.Pipeline[ReleaseArgs] { return &Release{} })
}
