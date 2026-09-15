package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type ReleaseArgs struct {
	Version string `flag:"version" desc:"Explicit release version (e.g. v0.24.0); sparkwing is locked to v0.x, so v1.0.0+ is refused. When empty, derived from latest origin tag + --bump."`
	Bump    string `flag:"bump" desc:"Auto-bump kind when --version is empty: patch|minor|major. Default: minor"`
}

type Release struct {
	sparkwing.Base
	args ReleaseArgs
}

func (Release) ShortHelp() string {
	return "Tag and push a public sparkwing version (kicks GH-Actions release)"
}

func (Release) Help() string {
	return "Cuts a release from the commit in the working tree: resolves the version, checks it is ahead of the newest tag origin carries, renames the CHANGELOG.md [Unreleased] section to it, rolls docs/migrations/_unreleased.md to vX.Y.Z.md with a fresh placeholder behind it, adds the index row, repoints the section's (Breaking) links at the rolled guide, commits all of that as one change, then pushes the branch and an annotated vX.Y.Z tag. It refuses to tag when a (Breaking) entry has no section in the guide being rolled, because that prose is written by a person. It refuses nothing about where origin's branch tip is. The .github/workflows/release.yaml workflow takes over from the tag push and checks nothing: it resolves the tag to a commit, builds the binaries and images, signs and publishes them, and creates the GitHub release from the tag's changelog section, falling back to the annotated tag message and then to a pointer at CHANGELOG.md when that source carries no section. A failed build publishes nothing; the fix is a later patch tag. Before it tags, the release cut runs its check class: build, the full linter and the fast test class in parallel, budgeted at five minutes, which fails the cut when it overruns. The race, Postgres, chaos and browser suites are the heavier classes that `gate` and `pre-release` run on demand and in hosted CI. This pipeline never builds or publishes artifacts itself."
}

func (Release) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Auto-pick version by bumping latest origin tag", Command: "SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --sw-allow destructive,prod"},
		{Comment: "Tag and push an explicit version", Command: "SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --version v0.24.0 --sw-allow destructive,prod"},
		{Comment: "Preview without pushing", Command: "SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --sw-dry-run"},
	}
}

func (r *Release) Plan(_ context.Context, plan *sparkwing.Plan, in ReleaseArgs, _ sparkwing.RunContext) error {
	r.args = in

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

	// safety: the class judges the tree the tag will carry, and it judges it
	// before prepare-changelog commits, so no lint or suite reads a checkout
	// that changes under it.
	cutChecks := sparkwing.Job(plan, "release-cut-checks", &releaseCutChecksJob{})

	changelog := sparkwing.Job(plan, "prepare-changelog", &prepareChangelogJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	changelog.Needs(discover, validate, clean, cutChecks)

	schemaGate := sparkwing.Job(plan, "gate-schema-changelog", &checkSchemaBreakJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	schemaGate.Needs(discover, changelog)

	wireGate := sparkwing.Job(plan, "gate-wire-changelog", &checkWireBreakJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	wireGate.Needs(discover, changelog)

	pushTag := sparkwing.Job(plan, "push-tag", &pushTagJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	pushTag.Needs(validate, clean, changelog, schemaGate, wireGate, cutChecks)
	return nil
}

type releaseCutChecksJob struct{ sparkwing.Base }

// perf: the release cut is the third check class. Build, the full linter and
// the fast test class run in parallel under one budget; the race, Postgres,
// chaos and browser suites are the heavier classes that hosted CI runs on
// every pull request and every push to main.
func (j *releaseCutChecksJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	w.ParallelFailures(sparkwing.FailFast)
	budget := newTierBudget("release cut", releaseCutBudget)
	budget.step(w, "build", runBuild)
	budget.step(w, "lint", runGolangciLint)
	budget.step(w, "test", runShortTest)
	budget.verdict(w)
	return nil, nil
}

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
	newest, err := latestSemverTagIn(ctx, j.RepoDir)
	if err != nil {
		return fmt.Errorf("release: resolve the newest tag on origin: %w", err)
	}
	if err := requireAheadOfNewestTag(version, newest); err != nil {
		return err
	}
	if err := j.checkHostingReleaseConstant(ctx, version); err != nil {
		return err
	}
	if newest == "" {
		sparkwing.Info(ctx, "origin carries no release tag; %s is the first", version)
		return nil
	}
	sparkwing.Info(ctx, "version %s is ahead of the newest tag on origin (%s)", version, newest)
	return nil
}

// safety: the whole release precondition. A tag may be cut from any commit as
// safety: long as its version outranks every published one, so nothing here
// safety: reads a branch or a remote tip.
func requireAheadOfNewestTag(version, newest string) error {
	if newest == "" {
		return nil
	}
	if semver.Compare(version, newest) > 0 {
		return nil
	}
	return fmt.Errorf("release: version %s is not ahead of %s, the newest tag on origin; "+
		"a release tag must outrank every published version (never force-push a module tag), so pick a higher one", version, newest)
}

const firstHostingReleaseSource = "internal/wingd/client/client.go"

var firstHostingReleasePattern = regexp.MustCompile(`(?m)^const FirstHostingRelease = "([^"]+)"`)

func (j *validateVersionJob) checkHostingReleaseConstant(ctx context.Context, version string) error {
	path := filepath.Join(j.RepoDir, firstHostingReleaseSource)
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read %s: %w", firstHostingReleaseSource, err)
	}
	m := firstHostingReleasePattern.FindSubmatch(src)
	if m == nil {
		return fmt.Errorf("release: no FirstHostingRelease declaration found in %s; "+
			"the daemon-hosting release gate cannot verify what a too-old host is told to install "+
			"(update firstHostingReleasePattern if the constant moved)", firstHostingReleaseSource)
	}
	declared := string(m[1])
	if version == declared {
		return nil
	}
	claimed, err := tagExistsOnRemote(ctx, j.RepoDir, declared)
	if err != nil {
		return fmt.Errorf("release: check daemon-hosting release tag: %w", err)
	}
	if claimed {
		return nil
	}
	return fmt.Errorf("release: cutting %s while FirstHostingRelease still names the unreleased %s. "+
		"That constant is what a pipeline binary tells an operator to install when their sparkwing is too old to host "+
		"the admission daemon, so shipping it unchanged would name a release that never carried the feature. "+
		"Set it to %s in %s (and update the migration note in CHANGELOG.md), or cut %s instead",
		version, declared, version, firstHostingReleaseSource, declared)
}

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

type prepareChangelogJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

const embeddedChangelogRel = "pkg/docs/changelog.md"

func embeddedChangelogPath(repoDir string) string {
	return filepath.Join(repoDir, filepath.FromSlash(embeddedChangelogRel))
}

func writeChangelogPair(repoDir, body string) error {
	if err := os.WriteFile(filepath.Join(repoDir, "CHANGELOG.md"), []byte(body), 0o644); err != nil {
		return fmt.Errorf("write CHANGELOG.md: %w", err)
	}
	if err := os.WriteFile(embeddedChangelogPath(repoDir), []byte(body), 0o644); err != nil {
		return fmt.Errorf("sync %s: %w", embeddedChangelogRel, err)
	}
	return nil
}

func (j *prepareChangelogJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).DryRun(j.dryRun)
	return nil, nil
}

func (j *prepareChangelogJob) run(ctx context.Context) error {
	version := j.Version.Get(ctx)
	body, action, err := j.planned(version)
	if err != nil {
		return err
	}
	switch action.kind {
	case rewriteNoop:
		sparkwing.Info(ctx, "CHANGELOG.md already has [%s] section (%d entries); skipping rewrite", version, action.versionEntries)
	case rewriteApply:
		sparkwing.Info(ctx, "renaming CHANGELOG.md [Unreleased] -> [%s] (%d entries)", version, action.unreleasedEntries)
	}
	roll, err := planMigrationRollIn(j.RepoDir, body, version, releaseDateFor(body, version))
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	staged := []string{"CHANGELOG.md", embeddedChangelogRel}
	switch {
	case roll.needed && roll.guideOnDisk:
		body = roll.changelogBody
		written, err := writeMigrationRoll(j.RepoDir, roll)
		if err != nil {
			return fmt.Errorf("release: %w", err)
		}
		staged = append(staged, written...)
		sparkwing.Info(ctx, "docs/migrations/%s was already written; repointed %d link(s), indexed it and reset _unreleased.md",
			roll.guideName, roll.repointed)
	case roll.needed:
		body = roll.changelogBody
		written, err := writeMigrationRoll(j.RepoDir, roll)
		if err != nil {
			return fmt.Errorf("release: %w", err)
		}
		staged = append(staged, written...)
		sparkwing.Info(ctx, "rolled docs/migrations/_unreleased.md -> %s (%d breaking entries, %d links repointed) and indexed it",
			roll.guideName, roll.breaking, roll.repointed)
	default:
		sparkwing.Info(ctx, "[%s] carries no (Breaking) entry, so %s ships without a migration guide", version, version)
	}
	if err := writeChangelogPair(j.RepoDir, body); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, append([]string{"add"}, staged...)...); err != nil {
		return fmt.Errorf("release: git add changelog: %w", err)
	}
	pending, err := runGitIn(ctx, j.RepoDir, "diff", "--cached", "--name-only")
	if err != nil {
		return fmt.Errorf("release: read the staged set: %w", err)
	}
	if strings.TrimSpace(pending) == "" {
		sparkwing.Info(ctx, "the rename and the guide are already committed for %s; nothing to commit", version)
		return nil
	}
	if _, err := runGitIn(ctx, j.RepoDir, "commit", "-m", releaseCommitSubject(version, roll.needed)); err != nil {
		return fmt.Errorf("release: git commit CHANGELOG.md: %w", err)
	}
	sparkwing.Info(ctx, "committed CHANGELOG.md rewrite for %s", version)
	return nil
}

func releaseCommitSubject(version string, rolled bool) string {
	if rolled {
		return "release: " + version + " changelog and migration guide"
	}
	return "release: " + version + " changelog"
}

// safety: the index row dates the release the changelog heading dates it, so a
// rerun over an already-renamed section does not claim a second date.
func releaseDateFor(body, version string) string {
	if d := releaseSectionDate(body, version); d != "" {
		return d
	}
	return time.Now().UTC().Format("2006-01-02")
}

func (j *prepareChangelogJob) planned(version string) (string, changelogRewrite, error) {
	body, err := os.ReadFile(filepath.Join(j.RepoDir, "CHANGELOG.md"))
	if err != nil {
		return "", changelogRewrite{}, fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	action, err := planChangelogRewrite(string(body), version)
	if err != nil {
		return "", changelogRewrite{}, fmt.Errorf("release: %w", err)
	}
	if action.kind == rewriteApply {
		return action.newBody, action, nil
	}
	return string(body), action, nil
}

func (j *prepareChangelogJob) dryRun(ctx context.Context) error {
	version := j.Version.Get(ctx)
	body, action, err := j.planned(version)
	if err != nil {
		return err
	}
	switch action.kind {
	case rewriteNoop:
		sparkwing.Info(ctx, "dry-run: CHANGELOG.md already has [%s] (%d entries); rewrite would be a no-op", version, action.versionEntries)
	case rewriteApply:
		sparkwing.Info(ctx, "dry-run: would rename [Unreleased] -> [%s] (%d entries) and commit", version, action.unreleasedEntries)
	}
	roll, err := planMigrationRollIn(j.RepoDir, body, version, releaseDateFor(body, version))
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	switch {
	case roll.needed && roll.guideOnDisk:
		sparkwing.Info(ctx, "dry-run: docs/migrations/%s is already written; would repoint %d link(s), reset _unreleased.md, and index it as %q",
			roll.guideName, roll.repointed, roll.summary)
	case roll.needed:
		sparkwing.Info(ctx, "dry-run: would rename docs/migrations/_unreleased.md -> %s for %d breaking entries, repoint %d link(s), write a fresh _unreleased.md, and index it as %q",
			roll.guideName, roll.breaking, roll.repointed, roll.summary)
	default:
		sparkwing.Info(ctx, "dry-run: [%s] carries no (Breaking) entry, so no migration guide would be rolled", version)
	}
	return nil
}

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

func rewriteUnreleasedToVersion(body, version, date string) (string, error) {
	re := regexp.MustCompile(`(?m)^## \[?Unreleased\]?\s*$`)
	loc := re.FindStringIndex(body)
	if loc == nil {
		return "", fmt.Errorf("CHANGELOG.md has no [Unreleased] heading to rewrite")
	}
	newHeader := "## [Unreleased]\n\n## [" + version + "] - " + date
	return body[:loc[0]] + newHeader + body[loc[1]:], nil
}

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
	if branch == "" || branch == "HEAD" {
		return errors.New("release: refusing to push from detached HEAD")
	}
	if branch != "main" {
		sparkwing.Info(ctx, "release: tagging from branch %q", branch)
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

func currentBranch(ctx context.Context, repoDir string) (string, error) {
	out, err := runGitIn(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// safety: the one release-tag grammar. bin/check-release-tag-order.sh and the
// safety: workflow's own shape check carry the same expression, and a test
// safety: pins all three, so no stage accepts a tag another stage refuses.
var releaseTagGrammar = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

func isReleaseTagShape(v string) bool {
	return releaseTagGrammar.MatchString(v)
}

func validateReleaseVersion(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("release: --version is required (e.g. --version v0.6.1)")
	}
	if !isReleaseTagShape(v) {
		return fmt.Errorf("release: version %q is not a vMAJOR.MINOR.PATCH release tag "+
			"(an optional -prerelease suffix is allowed; leading zeros and +build metadata are not)", v)
	}
	if semver.Prerelease(v) != "" {
		return fmt.Errorf("release: version %q is a pre-release; the pipeline only cuts stable tags", v)
	}
	// safety: module is locked to v0.x; remove this check to allow v1+ tags.
	if !onReleaseLine(v) {
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

// safety: v1.0.0 through v1.6.1 are retracted tombstone tags the module proxy
// safety: keeps forever (see the retract block in go.mod), so the release line
// safety: stops below them. Pre-releases do count: dropping one here would let
// safety: this pipeline cut a version behind one already published.
const releaseLineCeiling = "v1.0.0"

func onReleaseLine(tag string) bool {
	return isReleaseTagShape(tag) && semver.Compare(tag, releaseLineCeiling) < 0
}

func highestReleaseTag(tags []string) string {
	var best string
	for _, t := range tags {
		if !onReleaseLine(t) {
			continue
		}
		if best == "" || semver.Compare(t, best) > 0 {
			best = t
		}
	}
	return best
}

// safety: release-verify judges a tag that already exists, so the release
// safety: being judged is in its own tag list and must not be its own predecessor.
func previousReleaseTag(ctx context.Context, repoDir, version string) (string, error) {
	tags, err := remoteReleaseTags(ctx, repoDir)
	if err != nil {
		return "", err
	}
	tags = slices.DeleteFunc(tags, func(t string) bool { return t == version })
	return highestReleaseTag(tags), nil
}

func latestSemverTagIn(ctx context.Context, repoDir string) (string, error) {
	tags, err := remoteReleaseTags(ctx, repoDir)
	if err != nil {
		return "", err
	}
	return highestReleaseTag(tags), nil
}

func remoteReleaseTags(ctx context.Context, repoDir string) ([]string, error) {
	out, err := runGitIn(ctx, repoDir, "ls-remote", "--tags", "origin")
	if err != nil {
		return nil, err
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
	return tags, nil
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

const storeSchemaSourcePath = "pkg/store/store.go"

var storeSchemaConstRe = regexp.MustCompile(`(?m)^const\s+expectedSchemaVersion\s*=\s*(\d+)\b`)

var (
	migrationRequirementsDeclRe  = regexp.MustCompile(`(?m)^var\s+migrationRequirements\s*=`)
	migrationRequirementsBlockRe = regexp.MustCompile(`(?s)^var\s+migrationRequirements\s*=\s*map\[int\]\[\]string\{(|.*?\n)\}`)
	migrationRequirementsEntryRe = regexp.MustCompile(`(?m)^\s*(\d+)\s*:\s*\{([^}]*)\}`)
	migrationRequirementNameRe   = regexp.MustCompile(`"([^"]+)"`)
)

// safety: distinct from a registry the parser cannot read, which fails the gate;
// a tag cut before requirements shipped carries none at all.
var errNoRequirementRegistry = errors.New("no `var migrationRequirements = map[int][]string{...}`")

// safety: parses the source rather than importing the package, so the gate can read
// the registry at a released tag as well as the one being cut.
func parseMigrationRequirements(goSource string) (map[int][]string, error) {
	loc := migrationRequirementsDeclRe.FindStringIndex(goSource)
	if loc == nil {
		return nil, fmt.Errorf("%w in %s", errNoRequirementRegistry, storeSchemaSourcePath)
	}
	block := migrationRequirementsBlockRe.FindStringSubmatch(goSource[loc[0]:])
	if block == nil {
		return nil, fmt.Errorf("unterminated migrationRequirements registry in %s", storeSchemaSourcePath)
	}
	out := map[int][]string{}
	for _, entry := range migrationRequirementsEntryRe.FindAllStringSubmatch(block[1], -1) {
		version, err := strconv.Atoi(entry[1])
		if err != nil {
			return nil, fmt.Errorf("parse %s requirement version %q: %w", storeSchemaSourcePath, entry[1], err)
		}
		for _, name := range migrationRequirementNameRe.FindAllStringSubmatch(entry[2], -1) {
			out[version] = append(out[version], name[1])
		}
	}
	return out, nil
}

func requirementsAddedBetween(registry map[int][]string, prevSchema, curSchema int) []string {
	var added []string
	for v := prevSchema + 1; v <= curSchema; v++ {
		added = append(added, registry[v]...)
	}
	sort.Strings(added)
	return added
}

func requirementsAddedSince(prev, cur map[int][]string) []string {
	had := map[string]bool{}
	for _, names := range prev {
		for _, name := range names {
			had[name] = true
		}
	}
	var added []string
	for _, names := range cur {
		for _, name := range names {
			if !had[name] {
				added = append(added, name)
			}
		}
	}
	sort.Strings(added)
	return added
}

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
	return checkSchemaBreak(ctx, j.RepoDir, j.Version.Get(ctx))
}

func checkSchemaBreak(ctx context.Context, repoDir, version string) error {
	prevTag, err := previousReleaseTag(ctx, repoDir, version)
	if err != nil {
		return fmt.Errorf("release: resolve previous tag for schema gate: %w", err)
	}
	if prevTag == "" {
		sparkwing.Info(ctx, "no previous release tag; skipping schema-break changelog gate")
		return nil
	}
	curSrc, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(storeSchemaSourcePath)))
	if err != nil {
		return fmt.Errorf("release: read %s: %w", storeSchemaSourcePath, err)
	}
	curSchema, err := parseStoreSchemaVersion(string(curSrc))
	if err != nil {
		return fmt.Errorf("release: current schema: %w", err)
	}
	prevSrc, err := runGitIn(ctx, repoDir, "show", prevTag+":"+storeSchemaSourcePath)
	if err != nil {
		return fmt.Errorf("release: read %s at %s: %w", storeSchemaSourcePath, prevTag, err)
	}
	prevSchema, err := parseStoreSchemaVersion(prevSrc)
	if err != nil {
		return fmt.Errorf("release: schema at %s: %w", prevTag, err)
	}
	curRegistry, err := parseMigrationRequirements(string(curSrc))
	if err != nil {
		return fmt.Errorf("release: current schema requirements: %w", err)
	}
	added, err := requirementsAdded(prevSrc, curRegistry, prevSchema, curSchema)
	if err != nil {
		return fmt.Errorf("release: schema requirements at %s: %w", prevTag, err)
	}
	if prevSchema == curSchema && len(added) == 0 {
		sparkwing.Info(ctx, "runs-store schema unchanged since %s (schema %d) and no requirement added; gate passes", prevTag, curSchema)
		return nil
	}
	body, err := os.ReadFile(filepath.Join(repoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	issues := LintSchemaBreak(string(body), version, prevSchema, curSchema, added)
	if len(issues) > 0 {
		var b strings.Builder
		for _, i := range issues {
			b.WriteString(i.Format())
			b.WriteByte('\n')
		}
		return fmt.Errorf("release: undescribed runs-store schema change blocks %s:\n%s", version, b.String())
	}
	if len(added) > 0 {
		sparkwing.Info(ctx, "runs-store schema %d -> %d adds requirement(s) %s and is marked (Breaking) in the changelog; gate passes",
			prevSchema, curSchema, strings.Join(added, ", "))
		return nil
	}
	sparkwing.Info(ctx, "runs-store schema %d -> %d adds no requirement and carries a store changelog entry; gate passes", prevSchema, curSchema)
	return nil
}

// safety: a tag cut before the registry existed has no prior classification to diff,
// so only the versions this release adds count; diffing against it would demand a
// (Breaking) entry for the initial population.
func requirementsAdded(prevSrc string, curRegistry map[int][]string, prevSchema, curSchema int) ([]string, error) {
	prevRegistry, err := parseMigrationRequirements(prevSrc)
	if errors.Is(err, errNoRequirementRegistry) {
		return requirementsAddedBetween(curRegistry, prevSchema, curSchema), nil
	}
	if err != nil {
		return nil, err
	}
	return requirementsAddedSince(prevRegistry, curRegistry), nil
}

type checkWireBreakJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (j *checkWireBreakJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).SafeWithoutDryRun()
	return nil, nil
}

func (j *checkWireBreakJob) run(ctx context.Context) error {
	return checkWireBreak(ctx, j.RepoDir, j.Version.Get(ctx))
}

func checkWireBreak(ctx context.Context, repoDir, version string) error {
	prevTag, err := previousReleaseTag(ctx, repoDir, version)
	if err != nil {
		return fmt.Errorf("release: resolve previous tag for wire gate: %w", err)
	}
	if prevTag == "" {
		sparkwing.Info(ctx, "no previous release tag; skipping wire-surface changelog gate")
		return nil
	}
	cuts, err := wireCutsSince(ctx, repoDir, prevTag)
	if err != nil {
		return fmt.Errorf("release: diff the wire surface against %s: %w", prevTag, err)
	}
	if len(cuts) == 0 {
		sparkwing.Info(ctx, "wire surface added to or unchanged since %s; gate passes", prevTag)
		return nil
	}
	body, err := os.ReadFile(filepath.Join(repoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	issues := LintWireBreak(string(body), version, cuts, migrationsFS(repoDir))
	if len(issues) > 0 {
		var b strings.Builder
		for _, i := range issues {
			b.WriteString(i.Format())
			b.WriteByte('\n')
		}
		return fmt.Errorf("release: undeclared wire-surface cut blocks %s:\n%s", version, b.String())
	}
	sparkwing.Info(ctx, "wire surface cuts %s since %s and the changelog declares it; gate passes",
		strings.Join(describeCuts(cuts), ", "), prevTag)
	return nil
}

func wireCutsSince(ctx context.Context, repoDir, prevTag string) ([]wireCut, error) {
	states := make([]wireSurfaceState, 0, len(wireSurfaces))
	for _, surface := range wireSurfaces {
		prev, present, err := fileAtTag(ctx, repoDir, prevTag, surface.path)
		if err != nil {
			return nil, err
		}
		if !present {
			sparkwing.Info(ctx, "%s does not exist at %s; nothing to diff", surface.path, prevTag)
			states = append(states, wireSurfaceState{surface: surface})
			continue
		}
		cur, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(surface.path)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", surface.path, err)
		}
		states = append(states, wireSurfaceState{surface: surface, present: true, prev: prev, cur: string(cur)})
	}
	return wireCuts(states)
}

func fileAtTag(ctx context.Context, repoDir, tag, path string) (body string, present bool, err error) {
	listed, err := runGitIn(ctx, repoDir, "ls-tree", "--name-only", tag, "--", path)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(listed) == "" {
		return "", false, nil
	}
	body, err = runGitIn(ctx, repoDir, "show", tag+":"+path)
	if err != nil {
		return "", false, err
	}
	return body, true, nil
}

func init() {
	sparkwing.Register[ReleaseArgs]("release", func() sparkwing.Pipeline[ReleaseArgs] { return &Release{} })
}
