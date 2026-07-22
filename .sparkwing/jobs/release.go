package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	"golang.org/x/mod/sumdb/dirhash"

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
	plan.Resources(sparkwing.Cores(2), sparkwing.MemoryGB(4))

	repoDir, err := repoRoot()
	if err != nil {
		return fmt.Errorf("release: locate repo root: %w", err)
	}

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

	gatePreCommit := sparkwing.Job(plan, "gate-pre-commit", &PreCommit{})
	gatePreCommit.Needs(clean)

	gatePrePush := sparkwing.Job(plan, "gate-pre-push", func(ctx context.Context) error {
		return (&PrePush{AllowReleaseLineSelfReplace: true}).run(ctx)
	})
	gatePrePush.Needs(clean)

	gateTemplates := sparkwing.Job(plan, "gate-template-verify", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[TemplateVerifySummary, sparkwing.NoInputs](
			ctx, "template-verify", "summary",
			sparkwing.WithFreshTimeout(20*time.Minute),
		)
		return err
	})
	gateTemplates.Needs(clean)

	gateLineage := sparkwing.Job(plan, "gate-release-lineage", &checkReleaseLineageJob{
		RepoDir: repoDir,
	})

	changelog := sparkwing.Job(plan, "prepare-changelog", &prepareChangelogJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	changelog.Needs(discover, gatePreCommit, gatePrePush)

	bumpSelf := sparkwing.Job(plan, "bump-self-replace", &prepareSelfReplaceJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	bumpSelf.Needs(discover, gatePreCommit, gatePrePush, changelog)

	schemaGate := sparkwing.Job(plan, "gate-schema-changelog", &checkSchemaBreakJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	schemaGate.Needs(discover, changelog)

	pushTag := sparkwing.Job(plan, "push-tag", &pushTagJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	pushTag.Needs(validate, clean, changelog, bumpSelf, schemaGate, gateTemplates, gateLineage)

	restoreSelf := sparkwing.Job(plan, "restore-self-replace", &restoreSelfReplaceJob{
		RepoDir: repoDir,
	})
	restoreSelf.Needs(pushTag)
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

// selfReplaceComment is the comment block that precedes the dogfood
// self-replace in `.sparkwing/go.mod`. Kept verbatim so restore-side
// rewrites round-trip cleanly.
const selfReplaceComment = `// The pipelines tree is consumed as the same module path the SDK
// itself ships, so the require above is a placeholder; this replace
// pins it to the parent checkout (the sparkwing repo root). The
// pattern follows the standard "consumer .sparkwing/ uses a local
// replace during development" convention; here the parent IS the
// SDK rather than a sibling.
`

const selfReplaceLine = "replace github.com/sparkwing-dev/sparkwing => .."

const sparkwingModulePath = "github.com/sparkwing-dev/sparkwing"

// prepareSelfReplaceJob bumps the sparkwing pin in `.sparkwing/go.mod`
// to the release version and strips the local self-replace. Runs
// pre-tag so the shipped commit's `.sparkwing/go.mod` is in
// ready-to-ship shape (no relative-path replace, real version pin).
// Pairs with restoreSelfReplaceJob which puts the replace back after
// the tag is out.
type prepareSelfReplaceJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (j *prepareSelfReplaceJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).DryRun(j.dryRun)
	return nil, nil
}

func (j *prepareSelfReplaceJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	path := filepath.Join(j.RepoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	newBody, changed, err := stripSelfReplace(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if !changed {
		sparkwing.Info(ctx, ".sparkwing/go.mod already in shipped shape; skipping")
		return nil
	}
	if err := os.WriteFile(path, []byte(newBody), 0o644); err != nil {
		return fmt.Errorf("release: write .sparkwing/go.mod: %w", err)
	}
	if err := writeSelfModuleSums(ctx, j.RepoDir, version); err != nil {
		return err
	}
	if _, err := runGitIn(ctx, j.RepoDir, "add", ".sparkwing/go.mod", ".sparkwing/go.sum"); err != nil {
		return fmt.Errorf("release: git add .sparkwing module files: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "commit", "-m",
		"release: pin .sparkwing/ to "+version+", drop local self-replace"); err != nil {
		return fmt.Errorf("release: git commit .sparkwing module files: %w", err)
	}
	sparkwing.Info(ctx, "bumped .sparkwing/go.mod sparkwing pin -> %s, removed self-replace", version)
	return nil
}

func (j *prepareSelfReplaceJob) dryRun(ctx context.Context) error {
	version := j.Version.Get(ctx)
	path := filepath.Join(j.RepoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	_, changed, err := stripSelfReplace(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if !changed {
		sparkwing.Info(ctx, "dry-run: .sparkwing/go.mod already in shipped shape; no rewrite")
	} else {
		sparkwing.Info(ctx, "dry-run: would bump .sparkwing/go.mod pin to %s and strip self-replace", version)
	}
	return nil
}

func writeSelfModuleSums(ctx context.Context, repoDir, version string) error {
	zipHash, goModHash, err := selfModuleSums(ctx, repoDir, version)
	if err != nil {
		return fmt.Errorf("release: compute .sparkwing self-module sums: %w", err)
	}
	sumPath := filepath.Join(repoDir, ".sparkwing", "go.sum")
	body, err := os.ReadFile(sumPath)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.sum: %w", err)
	}
	linesByText := map[string]struct{}{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, sparkwingModulePath+" "+version+" ") ||
			strings.HasPrefix(line, sparkwingModulePath+" "+version+"/go.mod ") {
			continue
		}
		linesByText[line] = struct{}{}
	}
	linesByText[fmt.Sprintf("%s %s %s", sparkwingModulePath, version, zipHash)] = struct{}{}
	linesByText[fmt.Sprintf("%s %s/go.mod %s", sparkwingModulePath, version, goModHash)] = struct{}{}

	lines := make([]string, 0, len(linesByText))
	for line := range linesByText {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	if err := os.WriteFile(sumPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return fmt.Errorf("release: write .sparkwing/go.sum: %w", err)
	}
	return nil
}

func selfModuleSums(ctx context.Context, repoDir, version string) (string, string, error) {
	tmp, err := os.CreateTemp("", "sparkwing-release-module-*.zip")
	if err != nil {
		return "", "", err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	defer func() { _ = tmp.Close() }()

	moduleZip, err := createSelfModuleZip(ctx, repoDir, version)
	if err != nil {
		return "", "", err
	}
	if _, err := tmp.Write(moduleZip); err != nil {
		return "", "", err
	}
	if err := tmp.Close(); err != nil {
		return "", "", err
	}
	zipHash, err := dirhash.HashZip(tmpPath, dirhash.Hash1)
	if err != nil {
		return "", "", err
	}

	goMod, err := os.ReadFile(filepath.Join(repoDir, "go.mod"))
	if err != nil {
		return "", "", err
	}
	goModHash, err := dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(goMod)), nil
	})
	if err != nil {
		return "", "", err
	}
	return zipHash, goModHash, nil
}

func createSelfModuleZip(ctx context.Context, repoDir, version string) ([]byte, error) {
	escapedPath, err := module.EscapePath(sparkwingModulePath)
	if err != nil {
		return nil, err
	}
	escapedVersion, err := module.EscapeVersion(version)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "archive", "--format=zip", "--prefix="+escapedPath+"@"+escapedVersion+"/", "HEAD")
	cmd.Dir = repoDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("git archive HEAD: %w", err)
		}
		return nil, fmt.Errorf("git archive HEAD: %w: %s", err, msg)
	}
	return out, nil
}

// restoreSelfReplaceJob undoes prepareSelfReplaceJob's mutation after
// the tag has been pushed. Adds the self-replace block back and
// pushes the restore commit so subsequent local development picks up
// SDK edits via the parent checkout instead of the freshly-tagged
// module proxy version. Idempotent: noop if the replace is already
// present.
type restoreSelfReplaceJob struct {
	sparkwing.Base
	RepoDir string
}

func (j *restoreSelfReplaceJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).DryRun(j.dryRun).Risk("destructive")
	return nil, nil
}

func (j *restoreSelfReplaceJob) run(ctx context.Context) error {
	path := filepath.Join(j.RepoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	newBody, changed := restoreSelfReplace(string(body))
	if !changed {
		sparkwing.Info(ctx, ".sparkwing/go.mod self-replace already present; skipping")
		return nil
	}
	if err := os.WriteFile(path, []byte(newBody), 0o644); err != nil {
		return fmt.Errorf("release: write .sparkwing/go.mod: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "add", ".sparkwing/go.mod", ".sparkwing/go.sum"); err != nil {
		return fmt.Errorf("release: git add .sparkwing module files: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "commit", "-m",
		"chore: restore .sparkwing/ local self-replace for next dev cycle"); err != nil {
		return fmt.Errorf("release: git commit .sparkwing module files: %w", err)
	}
	branch, err := currentBranch(ctx, j.RepoDir)
	if err != nil {
		return fmt.Errorf("release: detect branch for restore push: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "push", "origin", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("release: push restore commit: %w", err)
	}
	sparkwing.Info(ctx, "restored .sparkwing/ self-replace + pushed to %s", branch)
	return nil
}

func (j *restoreSelfReplaceJob) dryRun(ctx context.Context) error {
	path := filepath.Join(j.RepoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	_, changed := restoreSelfReplace(string(body))
	if !changed {
		sparkwing.Info(ctx, "dry-run: .sparkwing/go.mod self-replace already present; no rewrite")
	} else {
		sparkwing.Info(ctx, "dry-run: would restore .sparkwing/ self-replace, commit, and push")
	}
	return nil
}

// stripSelfReplace rewrites .sparkwing/go.mod for release: bumps the
// `require github.com/sparkwing-dev/sparkwing vX.Y.Z` line to version
// and removes the comment-block-plus-replace-line trailer. Pure
// function so the rewrite is unit-testable without git or the file
// system. Returns (newBody, changed, err).
//
//   - If neither the require nor the replace is present: error
//     (unexpected go.mod shape; refuse to guess).
//   - If the replace is absent but the require is on `version` already:
//     (body, false, nil) -- already in shipped shape.
//   - Otherwise: bump require, strip the comment + replace trailer.
func stripSelfReplace(body, version string) (string, bool, error) {
	requireRe := regexp.MustCompile(`(?m)^([\t ]*(?:require[\t ]+)?)` + regexp.QuoteMeta(sparkwingModulePath) + `[\t ]+v[0-9][0-9A-Za-z.+-]*[\t ]*$`)
	if !requireRe.MatchString(body) {
		return "", false, fmt.Errorf(".sparkwing/go.mod: no `%s vX.Y.Z` require line found", sparkwingModulePath)
	}
	newBody := requireRe.ReplaceAllString(body, "${1}"+sparkwingModulePath+" "+version)

	replaceRe := regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(sparkwingModulePath) + `\s*=>\s*\.\.\s*$`)
	loc := replaceRe.FindStringIndex(newBody)
	if loc == nil {
		return newBody, newBody != body, nil
	}
	start := loc[0]
	for start > 0 {
		prevEnd := start - 1
		if prevEnd >= 0 && newBody[prevEnd] != '\n' {
			break
		}
		prevStart := prevEnd - 1
		for prevStart >= 0 && newBody[prevStart] != '\n' {
			prevStart--
		}
		line := newBody[prevStart+1 : prevEnd]
		if !strings.HasPrefix(line, "//") {
			break
		}
		start = prevStart + 1
	}
	if start >= 2 && newBody[start-1] == '\n' && newBody[start-2] == '\n' {
		start--
	}
	end := loc[1]
	if end < len(newBody) && newBody[end] == '\n' {
		end++
	}
	newBody = newBody[:start] + newBody[end:]
	return newBody, true, nil
}

// restoreSelfReplace puts the dogfood self-replace block back after a
// release cut. Idempotent: returns (body, false) if the replace is
// already present.
func restoreSelfReplace(body string) (string, bool) {
	replaceRe := regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(sparkwingModulePath) + `\s*=>\s*\.\.\s*$`)
	if replaceRe.MatchString(body) {
		return body, false
	}
	trimmed := strings.TrimRight(body, "\n")
	return trimmed + "\n\n" + selfReplaceComment + selfReplaceLine + "\n", true
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
			rest = strings.TrimSpace(rest)
			rest = strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
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
		sparkwing.Info(ctx, "release: tagging from branch %q", branch)
	}
	if err := ensureBranchContainsRemote(ctx, j.RepoDir, branch); err != nil {
		return err
	}
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
// HEAD. Detached HEAD returns "HEAD"; the release branch fence refuses
// that before pushing a branch or tag.
func currentBranch(ctx context.Context, repoDir string) (string, error) {
	out, err := runGitIn(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func ensureBranchContainsRemote(ctx context.Context, repoDir, branch string) error {
	if branch == "" || branch == "HEAD" {
		return fmt.Errorf("release: refusing to push from detached HEAD")
	}
	if _, err := runGitIn(ctx, repoDir, "fetch", "--quiet", "origin", branch); err != nil {
		return fmt.Errorf("release: fetch origin/%s before tag push: %w", branch, err)
	}
	remoteRef := "origin/" + branch
	if _, err := runGitIn(ctx, repoDir, "rev-parse", "--verify", "--quiet", remoteRef); err != nil {
		return fmt.Errorf("release: remote branch %s does not exist; push the branch before releasing", remoteRef)
	}
	if _, err := runGitIn(ctx, repoDir, "merge-base", "--is-ancestor", remoteRef, "HEAD"); err != nil {
		return fmt.Errorf("release: local %s does not contain %s; pull/rebase before releasing", branch, remoteRef)
	}
	return nil
}

// checkReleaseLineageJob refuses to cut a release from a line whose
// history does not contain the latest published release. That state
// means an earlier release was cut from a branch that never landed
// here; shipping over it would silently drop that release's work.
type checkReleaseLineageJob struct {
	sparkwing.Base
	RepoDir string
}

func (j *checkReleaseLineageJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
	return nil, nil
}

func (j *checkReleaseLineageJob) run(ctx context.Context) error {
	return ensureLineageContainsLatestRelease(ctx, j.RepoDir)
}

// ensureLineageContainsLatestRelease verifies the latest release tag on
// origin is an ancestor of HEAD. Tags are read from origin (never the
// local tag list) so a stale checkout cannot pass; the retracted v1.x
// tombstone line is excluded by the same rule the bump resolver uses. A
// repo with no release tags passes: there is no lineage to contain yet.
func ensureLineageContainsLatestRelease(ctx context.Context, repoDir string) error {
	latest, err := latestSemverTagIn(ctx, repoDir)
	if err != nil {
		return fmt.Errorf("release: resolve latest release tag: %w", err)
	}
	if latest == "" {
		return nil
	}
	if _, err := runGitIn(ctx, repoDir, "fetch", "--quiet", "origin", "refs/tags/"+latest); err != nil {
		return fmt.Errorf("release: fetch tag %s for lineage check: %w", latest, err)
	}
	sha, err := runGitIn(ctx, repoDir, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("release: resolve %s commit: %w", latest, err)
	}
	sha = strings.TrimSpace(sha)
	_, err = sparkwing.Exec(ctx, "git", "merge-base", "--is-ancestor", sha, "HEAD").Dir(repoDir).Run()
	if err == nil {
		sparkwing.Info(ctx, "history contains the latest release %s", latest)
		return nil
	}
	var ee *sparkwing.ExecError
	if errors.As(err, &ee) && ee.ExitCode == 1 {
		return fmt.Errorf("release: the latest release %s is not in this line's history. "+
			"An earlier release was cut from a branch that never landed here, so releasing now would ship without that work and silently drop it. "+
			"Bring the %s line back first -- `git fetch --tags origin && git log %s --not HEAD` lists the missing commits; merge or cherry-pick them -- then re-run",
			latest, latest, latest)
	}
	return fmt.Errorf("release: lineage check for %s: %w", latest, err)
}

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
	// safety: module is locked to v0.x; remove this check to allow v1+ tags.
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

// releaseTagCeiling is the exclusive upper bound on tags the release
// resolver treats as real releases. sparkwing is locked to the v0.x line
// (validateReleaseVersion refuses v1.0.0+), so any v1.0.0+ tag is a
// retracted tombstone -- notably v1.6.1, kept only to hold the Go module
// @latest pointer on the v0.x line -- never a real release. Picking one as
// the prev/latest release gives the schema gate a phantom prevSchema and
// the --bump baseline a wrong floor, so they are skipped here.
const releaseTagCeiling = "v1.0.0"

// highestReleaseTag returns the highest stable-semver release tag in tags,
// skipping pre-release/build tags and any tag at or above releaseTagCeiling
// (the retracted v1.x line). tags are bare tag names (e.g. "v0.11.0").
// Returns "" when no eligible tag exists. Pure so the resolver can be tested
// without git or a remote.
func highestReleaseTag(tags []string) string {
	var best string
	for _, t := range tags {
		if !semver.IsValid(t) {
			continue
		}
		if semver.Prerelease(t) != "" || semver.Build(t) != "" {
			continue
		}
		if semver.Compare(t, releaseTagCeiling) >= 0 {
			continue
		}
		if best == "" || semver.Compare(t, best) > 0 {
			best = t
		}
	}
	return best
}

// latestSemverTagIn returns the highest stable-semver release tag visible on
// origin, excluding the retracted v1.x line (see highestReleaseTag). Reads
// via `ls-remote --tags` rather than `git tag --list` so stale local tags
// (orphans from before an OSS scrub, force-deleted upstream refs, etc.)
// can't bias the bump fallback. The release pipeline's "what's the next
// version" decision is fundamentally a statement about what the world has
// seen -- not what this checkout happens to remember.
func latestSemverTagIn(ctx context.Context, repoDir string) (string, error) {
	out, err := runGitIn(ctx, repoDir, "ls-remote", "--tags", "origin")
	if err != nil {
		return "", err
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ref := fields[1]
		const prefix = "refs/tags/"
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		tags = append(tags, strings.TrimSuffix(strings.TrimPrefix(ref, prefix), "^{}"))
	}
	return highestReleaseTag(tags), nil
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

// storeSchemaSourcePath is the source file holding the embedded
// runs-store schema constant, read at HEAD and at the previous release
// tag to detect a schema bump. Mirrors what bin/check-release-schema-
// parity.sh compiles; reading the constant straight from source avoids
// building a binary per release tag.
const storeSchemaSourcePath = "pkg/store/store.go"

var storeSchemaConstRe = regexp.MustCompile(`(?m)^const\s+expectedSchemaVersion\s*=\s*(\d+)\b`)

// parseStoreSchemaVersion extracts the `expectedSchemaVersion` constant
// from pkg/store/store.go source. Pure so the release gate can be tested
// without git or a build.
func parseStoreSchemaVersion(goSource string) (int, error) {
	m := storeSchemaConstRe.FindStringSubmatch(goSource)
	if m == nil {
		return 0, fmt.Errorf("no `const expectedSchemaVersion = N` in %s", storeSchemaSourcePath)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("parse %s schema version %q: %w", storeSchemaSourcePath, m[1], err)
	}
	return n, nil
}

// checkSchemaBreakJob refuses a release whose runs-store schema changed
// since the previous tag without a matching `(Breaking)` changelog entry.
// It reads the schema constant at HEAD (working tree) and at the latest
// origin tag, and when they differ requires LintSchemaBreak to find a
// marked schema entry in the section being cut. The first release (no
// prior tag) has nothing to compare and passes.
type checkSchemaBreakJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (j *checkSchemaBreakJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
	return nil, nil
}

func (j *checkSchemaBreakJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	prevTag, err := latestSemverTagIn(ctx, j.RepoDir)
	if err != nil {
		return fmt.Errorf("release: resolve previous tag for schema gate: %w", err)
	}
	if prevTag == "" {
		sparkwing.Info(ctx, "no previous release tag; skipping schema-break changelog gate")
		return nil
	}
	curSrc, err := os.ReadFile(filepath.Join(j.RepoDir, filepath.FromSlash(storeSchemaSourcePath)))
	if err != nil {
		return fmt.Errorf("release: read %s: %w", storeSchemaSourcePath, err)
	}
	curSchema, err := parseStoreSchemaVersion(string(curSrc))
	if err != nil {
		return fmt.Errorf("release: current schema: %w", err)
	}
	prevSrc, err := runGitIn(ctx, j.RepoDir, "show", prevTag+":"+storeSchemaSourcePath)
	if err != nil {
		return fmt.Errorf("release: read %s at %s: %w", storeSchemaSourcePath, prevTag, err)
	}
	prevSchema, err := parseStoreSchemaVersion(prevSrc)
	if err != nil {
		return fmt.Errorf("release: schema at %s: %w", prevTag, err)
	}
	if prevSchema == curSchema {
		sparkwing.Info(ctx, "runs-store schema unchanged since %s (schema %d); gate passes", prevTag, curSchema)
		return nil
	}
	body, err := os.ReadFile(filepath.Join(j.RepoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	issues := LintSchemaBreak(string(body), version, prevSchema, curSchema)
	if len(issues) > 0 {
		var b strings.Builder
		for _, i := range issues {
			b.WriteString(i.Format())
			b.WriteByte('\n')
		}
		return fmt.Errorf("release: unmarked runs-store schema change blocks %s:\n%s", version, b.String())
	}
	sparkwing.Info(ctx, "runs-store schema %d -> %d is marked (Breaking) in the changelog; gate passes", prevSchema, curSchema)
	return nil
}

func init() {
	sparkwing.Register[ReleaseArgs]("release", func() sparkwing.Pipeline[ReleaseArgs] { return &Release{} })
}
