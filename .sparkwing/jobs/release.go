package jobs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"golang.org/x/mod/semver"
)

// ReleaseArgs is the typed CLI surface for the public-sparkwing
// release pipeline. --version is optional: when omitted the
// pipeline prefers the highest unreleased CHANGELOG.md entry, then
// falls back to bumping --bump (default minor) off the latest tag.
//
// Preview / no-mutation mode is delivered through wing's reserved
// `--dry-run` flag (IMP-014); each step below either marks itself
// SafeWithoutDryRun (read-only checks) or provides a .DryRun(...)
// body (the tag push), so the pipeline doesn't carry its own flag.
type ReleaseArgs struct {
	Version string `flag:"version" desc:"Explicit release version (e.g. v1.5.5). When empty, derived from CHANGELOG.md or latest tag + --bump."`
	Bump    string `flag:"bump" desc:"Auto-bump kind when --version is empty: patch|minor|major. Default: minor"`
}

// Release tags and pushes a new public-sparkwing version. The tag
// push triggers `.github/workflows/release.yaml`, which builds
// cross-platform binaries (for GitHub Releases) and multi-arch
// container images (for GHCR). This pipeline does NOT duplicate
// that work -- its job is to validate the release shape (clean
// tree, free tag, CHANGELOG entry) and push a single tag, then
// step out of the way.
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
	return "Validates the release shape (clean tree, free tag, CHANGELOG entry) and pushes a vX.Y.Z tag to origin. The .github/workflows/release.yaml workflow takes over from the tag push to build cross-platform binaries (uploaded to GH Releases) and multi-arch container images (published to GHCR). This pipeline never builds or publishes artifacts itself."
}

func (Release) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Auto-pick version from CHANGELOG / tag bump", Command: "wing release"},
		{Comment: "Tag and push an explicit version", Command: "wing release --version v1.5.5"},
		{Comment: "Preview without pushing", Command: "wing release --dry-run"},
	}
}

func (r *Release) Plan(_ context.Context, plan *sparkwing.Plan, in ReleaseArgs, _ sparkwing.RunContext) error {
	r.args = in

	repoDir, err := repoRoot()
	if err != nil {
		return fmt.Errorf("release: locate repo root: %w", err)
	}

	// All git/changelog probing happens inside Jobs (SDK-012). The
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

	changelog := sparkwing.Job(plan, "check-changelog", &checkChangelogJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	changelog.Needs(discover)

	pushTag := sparkwing.Job(plan, "push-tag", &pushTagJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	pushTag.Needs(validate, clean, changelog)
	return nil
}

// repoRoot returns the working directory of the wing run. In the
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
//  2. CHANGELOG-driven    -> highest version present in CHANGELOG.md
//     that doesn't yet have a tag on origin.
//  3. tag + bump          -> bump kind off the latest semver tag.
//
// First-ever release (no tags AND no CHANGELOG entries) yields
// v0.1.0 from the bump fallback.
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
	if v, err := highestUnreleasedFromChangelog(ctx, j.RepoDir); err != nil {
		fmt.Fprintf(os.Stderr, "release: changelog scan failed (using tag-bump fallback): %v\n", err)
	} else if v != "" {
		if err := validateReleaseVersion(v); err != nil {
			return "", fmt.Errorf("release: CHANGELOG points at %s but that's not valid semver: %w", v, err)
		}
		sparkwing.Info(ctx, "resolved from CHANGELOG.md: %s", v)
		return v, nil
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

// checkChangelogJob refuses to release a version without a matching
// entry in CHANGELOG.md. Forces release authorship to include user-
// facing notes -- silent releases turn into "what changed?" support
// questions later.
type checkChangelogJob struct {
	sparkwing.Base
	Version sparkwing.Ref[string]
	RepoDir string
}

func (j *checkChangelogJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
	return nil, nil
}

func (j *checkChangelogJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	path := filepath.Join(j.RepoDir, "CHANGELOG.md")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("changelog: open %s: %w", path, err)
	}
	defer f.Close()

	if found, headingLine := scanForVersion(f, version); found {
		sparkwing.Info(ctx, "changelog entry present: %s -> %s", version, strings.TrimSpace(headingLine))
		return nil
	}

	return fmt.Errorf(
		"changelog: no entry for %s in CHANGELOG.md\n"+
			"  add a section like:\n\n"+
			"    ## [%s] - YYYY-MM-DD\n\n"+
			"    ### Added\n"+
			"    - what's new for users\n\n"+
			"  then re-run the release. The pipeline refuses to ship a\n"+
			"  version without user-facing notes.",
		version, version)
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
	sparkwing.Step(w, "run", j.run).DryRun(j.dryRun)
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
	if _, err := runGitIn(ctx, j.RepoDir, "tag", "-a", version, "-m", "Release "+version); err != nil {
		return fmt.Errorf("release: create tag: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "push", "origin", "refs/tags/"+version); err != nil {
		return fmt.Errorf("release: push tag: %w", err)
	}
	sparkwing.Info(ctx, "pushed %s to origin (GH-Actions release.yaml will take over)", version)
	return nil
}

func (j *pushTagJob) dryRun(ctx context.Context) error {
	version := j.Version.Get(ctx)
	sparkwing.Info(ctx, "dry-run: would tag %s and push refs/tags/%s to origin", version, version)
	return nil
}

// --- helpers (kept local; cross-module helper imports don't work
// for the .sparkwing/ tree since it's a separate Go module). ---

func validateReleaseVersion(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("release: --version is required (e.g. --version v1.5.5)")
	}
	if !strings.HasPrefix(v, "v") {
		return fmt.Errorf("release: version %q must begin with 'v' (e.g. v1.5.5)", v)
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

func latestSemverTagIn(ctx context.Context, repoDir string) (string, error) {
	out, err := runGitIn(ctx, repoDir, "tag", "--list")
	if err != nil {
		return "", err
	}
	var best string
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || !semver.IsValid(t) {
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

func highestUnreleasedFromChangelog(ctx context.Context, repoDir string) (string, error) {
	versions, err := changelogVersions(repoDir)
	if err != nil {
		return "", err
	}
	if len(versions) == 0 {
		return "", nil
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return semver.Compare(versions[i], versions[j]) > 0
	})
	top := versions[0]
	exists, err := tagExistsOnRemote(ctx, repoDir, top)
	if err != nil {
		return "", fmt.Errorf("check remote tag %s: %w", top, err)
	}
	if exists {
		return "", nil
	}
	return top, nil
}

func changelogVersions(repoDir string) ([]string, error) {
	path := filepath.Join(repoDir, "CHANGELOG.md")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var out []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		trim := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(trim, "#") {
			continue
		}
		body := strings.TrimSpace(strings.TrimLeft(trim, "#"))
		for _, tok := range strings.Fields(body) {
			cleaned := strings.Trim(tok, "[]()*_`,;:.")
			if !semver.IsValid(cleaned) {
				continue
			}
			if semver.Prerelease(cleaned) != "" || semver.Build(cleaned) != "" {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(cleaned, "v"), ".")
			if len(parts) != 3 {
				continue
			}
			out = append(out, cleaned)
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

func scanForVersion(r *os.File, version string) (bool, string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "#") {
			continue
		}
		body := strings.TrimLeft(trim, "#")
		body = strings.TrimSpace(body)
		for _, tok := range strings.Fields(body) {
			cleaned := strings.Trim(tok, "[]()*_`,;:.")
			if cleaned == version {
				return true, line
			}
		}
	}
	return false, ""
}

func init() {
	sparkwing.Register[ReleaseArgs]("release", func() sparkwing.Pipeline[ReleaseArgs] { return &Release{} })
}
