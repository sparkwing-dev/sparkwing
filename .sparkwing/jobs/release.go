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
	modzip "golang.org/x/mod/zip"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const templateVerifyReleaseTimeout = time.Hour

// SAFETY: Release verification reruns every template without reusing recorded results.
var releaseTemplateVerifyArgs = TemplateVerifyArgs{Exhaustive: true}

type ReleaseArgs struct {
	Version string `flag:"version" desc:"Explicit release version (e.g. v0.24.0); sparkwing is locked to v0.x, so v1.0.0+ is refused. When empty, derived from latest origin tag + --bump."`
	Bump    string `flag:"bump" desc:"Auto-bump kind when --version is empty: patch|minor|major. Default: minor"`
}

type Release struct {
	sparkwing.Base
	arguments ReleaseArgs
}

func (Release) ShortHelp() string {
	return "Tag and push a public sparkwing version (starts release builds)"
}

func (Release) Help() string {
	return "Checks documentation, help, registry, and environment-variable contracts before pre-commit, pre-push, and exhaustive template verification. Requires a clean working tree, an available version tag, release notes, and release ancestry. Commits the changelog version, then pushes the branch and tag. Updates SDK pins and API snapshots, restores the local SDK replacement, and pushes those commits. The tag starts GitHub Actions builds for release binaries and container images."
}

func (Release) Examples() []sparkwing.Example {
	return []sparkwing.Example{
		{Comment: "Auto-pick version by bumping latest origin tag", Command: "SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --sw-allow destructive,prod"},
		{Comment: "Tag and push an explicit version", Command: "SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --version v0.24.0 --sw-allow destructive,prod"},
		{Comment: "Preview without pushing", Command: "SPARKWING_HOME=\"$(mktemp -d)\" sparkwing run release --sw-dry-run"},
	}
}

func (release *Release) Plan(_ context.Context, plan *sparkwing.Plan, arguments ReleaseArgs, _ sparkwing.RunContext) error {
	release.arguments = arguments

	repoDir, err := repoRoot()
	if err != nil {
		return fmt.Errorf("release: locate repo root: %w", err)
	}

	discover := sparkwing.Job(plan, "discover-version", &resolveVersionJob{
		Explicit: release.arguments.Version,
		Bump:     release.arguments.Bump,
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

	gateContracts := sparkwing.Job(plan, "gate-contracts", &checkContractsJob{RepoDir: repoDir})
	gateContracts.Needs(clean)

	gatePreCommit := sparkwing.Job(plan, "gate-pre-commit", &PreCommit{}).Timeout(preCommitTimeout)
	gatePreCommit.Needs(clean, gateContracts)

	gatePrePush := sparkwing.Job(plan, "gate-pre-push", func(ctx context.Context) error {
		return (&PrePush{AllowReleaseLineSelfReplace: true}).run(ctx)
	}).Timeout(prePushTimeout)
	gatePrePush.Needs(clean, gatePreCommit)

	gateTemplates := sparkwing.Job(plan, "gate-template-verify", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[TemplateVerifySummary, TemplateVerifyArgs](
			ctx, "template-verify", "summary",
			sparkwing.WithFreshInputs(releaseTemplateVerifyArgs),
			sparkwing.WithFreshTimeout(templateVerifyReleaseTimeout),
		)
		return err
	}).Resources(sparkwing.Cores(0.5))
	gateTemplates.Needs(clean, gatePreCommit, gatePrePush)

	gateLineage := sparkwing.Job(plan, "gate-release-lineage", &checkReleaseLineageJob{
		RepoDir: repoDir,
	})

	changelog := sparkwing.Job(plan, "prepare-changelog", &prepareChangelogJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	changelog.Needs(discover, gatePreCommit, gatePrePush, gateTemplates, gateLineage)

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
	pushTag.Needs(validate, clean, changelog, schemaGate, wireGate, gateTemplates, gateLineage)

	bumpSelf := sparkwing.Job(plan, "bump-self-replace", &prepareSelfReplaceJob{
		RepoDir: repoDir,
		Version: versionRef,
	})
	bumpSelf.Needs(discover, gatePreCommit, gatePrePush, gateTemplates, changelog, pushTag)
	bumpSelf.ContinueOnError()

	restoreSelf := sparkwing.Job(plan, "restore-self-replace", &restoreSelfReplaceJob{
		RepoDir: repoDir,
	})
	restoreSelf.Needs(bumpSelf)
	return nil
}

func repoRoot() (string, error) {
	directory := sparkwing.WorkDir()
	if directory == "" {
		return "", errors.New("sparkwing.WorkDir() returned empty")
	}
	if _, err := os.Stat(filepath.Join(directory, ".git")); err != nil {
		return "", fmt.Errorf("not a git repo at %s: %w", directory, err)
	}
	return directory, nil
}

type resolveVersionJob struct {
	sparkwing.Base
	sparkwing.Produces[string]

	Explicit string
	Bump     string
	RepoDir  string
}

func (job *resolveVersionJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "run", job.run).SafeWithoutDryRun(), nil
}

func (job *resolveVersionJob) run(ctx context.Context) (string, error) {
	if explicitVersion := strings.TrimSpace(job.Explicit); explicitVersion != "" {
		if err := validateReleaseVersion(explicitVersion); err != nil {
			return "", err
		}
		sparkwing.Info(ctx, "using explicit version: %s", explicitVersion)
		return explicitVersion, nil
	}
	bump := strings.TrimSpace(job.Bump)
	if bump == "" {
		bump = "minor"
	}
	switch bump {
	case "patch", "minor", "major":
	default:
		return "", fmt.Errorf("release: --bump must be patch|minor|major (got %q)", bump)
	}
	latest, err := latestSemverTagIn(ctx, job.RepoDir)
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

func (job *validateVersionJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).SafeWithoutDryRun()
	return nil, nil
}

func (job *validateVersionJob) run(ctx context.Context) error {
	version := job.Version.Get(ctx)
	if err := validateReleaseVersion(version); err != nil {
		return err
	}
	exists, err := tagExistsOnRemote(ctx, job.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: check remote tags: %w", err)
	}
	if exists {
		return fmt.Errorf("release: tag %s already exists on origin (never force-push a module tag; increment to a new version)", version)
	}
	if err := job.checkHostingReleaseConstant(ctx, version); err != nil {
		return err
	}
	sparkwing.Info(ctx, "version %s is free on origin", version)
	return nil
}

const firstHostingReleaseSource = "internal/wingd/client/client.go"

var firstHostingReleasePattern = regexp.MustCompile(`(?m)^const FirstHostingRelease = "([^"]+)"`)

func (job *validateVersionJob) checkHostingReleaseConstant(ctx context.Context, version string) error {
	path := filepath.Join(job.RepoDir, firstHostingReleaseSource)
	source, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read %s: %w", firstHostingReleaseSource, err)
	}
	match := firstHostingReleasePattern.FindSubmatch(source)
	if match == nil {
		return fmt.Errorf("release: no FirstHostingRelease declaration found in %s; verify the declaration and its parser", firstHostingReleaseSource)
	}
	declared := string(match[1])
	if version == declared {
		return nil
	}
	claimed, err := tagExistsOnRemote(ctx, job.RepoDir, declared)
	if err != nil {
		return fmt.Errorf("release: check daemon-hosting release tag: %w", err)
	}
	if claimed {
		return nil
	}
	return fmt.Errorf("release: version %s differs from unpublished FirstHostingRelease %s; set the constant to %s in %s and update its changelog note, or release %s",
		version, declared, version, firstHostingReleaseSource, declared)
}

type checkCleanTreeJob struct {
	sparkwing.Base
	RepoDir string
}

func (job *checkCleanTreeJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).SafeWithoutDryRun()
	return nil, nil
}

func (job *checkCleanTreeJob) run(ctx context.Context) error {
	output, err := runGitIn(ctx, job.RepoDir, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("release: git status: %w", err)
	}
	if strings.TrimSpace(output) != "" {
		return fmt.Errorf("release: working tree is dirty:\n%s\ncommit or stash before releasing", strings.TrimSpace(output))
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

func (job *prepareChangelogJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).DryRun(job.dryRun)
	return nil, nil
}

func (job *prepareChangelogJob) run(ctx context.Context) error {
	version := job.Version.Get(ctx)
	path := filepath.Join(job.RepoDir, "CHANGELOG.md")
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
	if err := writeChangelogPair(job.RepoDir, action.newBody); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if _, err := runGitIn(ctx, job.RepoDir, "add", "CHANGELOG.md", embeddedChangelogRel); err != nil {
		return fmt.Errorf("release: git add changelog: %w", err)
	}
	if _, err := runGitIn(ctx, job.RepoDir, "commit", "-m", "release: "+version+" changelog"); err != nil {
		return fmt.Errorf("release: git commit CHANGELOG.md: %w", err)
	}
	sparkwing.Info(ctx, "committed CHANGELOG.md rewrite for %s", version)
	return nil
}

func (job *prepareChangelogJob) dryRun(ctx context.Context) error {
	version := job.Version.Get(ctx)
	path := filepath.Join(job.RepoDir, "CHANGELOG.md")
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

const selfReplaceComment = `// SAFETY: Pipeline jobs use the parent checkout to exercise SDK source changes.
`

const selfReplaceLine = "replace github.com/sparkwing-dev/sparkwing => .."

const sparkwingModulePath = "github.com/sparkwing-dev/sparkwing"

type prepareSelfReplaceJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (job *prepareSelfReplaceJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).DryRun(job.dryRun)
	return nil, nil
}

func (job *prepareSelfReplaceJob) run(ctx context.Context) error {
	return bumpSelfReplace(ctx, job.RepoDir, job.Version.Get(ctx))
}

func bumpSelfReplace(ctx context.Context, repoDir, version string) error {
	path := filepath.Join(repoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	newBody, changed, err := stripSelfReplace(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	pinned, err := readFallbackSDKVersionFile(repoDir)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	fixturePinned, err := readPipelineModulePin(repoDir, kubernetesE2EPipelineModuleRel)
	if err != nil {
		return fmt.Errorf("release: read Kubernetes pipeline fixture pin: %w", err)
	}
	fallbackChanged := pinned != version
	fixtureChanged := fixturePinned != version
	aligned, err := releaseVersionArtifactsAligned(repoDir, version)
	if err != nil {
		return fmt.Errorf("release: inspect release-version artifacts: %w", err)
	}
	if !changed && aligned {
		sparkwing.Info(ctx, "release-version artifacts already match the release version; skipping")
		return nil
	}
	if changed {
		// #nosec G703 -- a pipeline job writing under the repository it runs in
		if err := os.WriteFile(path, []byte(newBody), 0o644); err != nil {
			return fmt.Errorf("release: write .sparkwing/go.mod: %w", err)
		}
		if err := writeSelfModuleSums(ctx, repoDir, version); err != nil {
			return err
		}
	}
	if fallbackChanged {
		if err := bumpFallbackSDKVersionFile(repoDir, version); err != nil {
			return fmt.Errorf("release: bump scaffold fallback: %w", err)
		}
	}
	if fixtureChanged {
		if err := bumpPipelineModulePin(ctx, repoDir, kubernetesE2EPipelineModuleRel, version); err != nil {
			return fmt.Errorf("release: bump Kubernetes pipeline fixture: %w", err)
		}
	}
	if err := regenerateScaffoldAPISnapshot(ctx, repoDir); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	addArgs := append([]string{"add", "--"}, sparkwingPinArtifacts...)
	if _, err := runGitIn(ctx, repoDir, addArgs...); err != nil {
		return fmt.Errorf("release: git add release-version artifacts: %w", err)
	}
	if _, err := runGitIn(ctx, repoDir, "commit", "-m",
		"release: pin SDK artifacts to "+version+", drop local self-replace"); err != nil {
		return fmt.Errorf("release: git commit release-version artifacts: %w", err)
	}
	sparkwing.Info(ctx, "bumped .sparkwing/go.mod and scaffold fallback -> %s, removed self-replace", version)
	return nil
}

func (job *prepareSelfReplaceJob) dryRun(ctx context.Context) error {
	version := job.Version.Get(ctx)
	path := filepath.Join(job.RepoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	_, moduleChanged, err := stripSelfReplace(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	aligned, err := releaseVersionArtifactsAligned(job.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if !moduleChanged && aligned {
		sparkwing.Info(ctx, "dry-run: release-version artifacts already match the release version; no rewrite")
	} else {
		sparkwing.Info(ctx, "%s", releaseVersionArtifactsDryRunMessage(version))
	}
	return nil
}

func releaseVersionArtifactsDryRunMessage(version string) string {
	return "dry-run: would align release-version artifacts to " + version
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
	temporaryFile, err := os.CreateTemp("", "sparkwing-release-module-*.zip")
	if err != nil {
		return "", "", err
	}
	temporaryPath := temporaryFile.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	defer func() { _ = temporaryFile.Close() }()

	moduleZip, err := createSelfModuleZip(ctx, repoDir, version)
	if err != nil {
		return "", "", err
	}
	if _, err := temporaryFile.Write(moduleZip); err != nil {
		return "", "", err
	}
	if err := temporaryFile.Close(); err != nil {
		return "", "", err
	}
	zipHash, err := dirhash.HashZip(temporaryPath, dirhash.Hash1)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := selfModuleZipFiles(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := modzip.Create(&output, module.Version{Path: sparkwingModulePath, Version: version}, files); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func selfModuleZipFiles(ctx context.Context, repoDir string) ([]modzip.File, error) {
	output, err := runGitRawIn(ctx, repoDir, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("release: list tracked files: %w", err)
	}
	paths := gitTrackedPaths(string(output))
	nestedModules := map[string]struct{}{}
	for _, path := range paths {
		if path == "" || path == "go.mod" || filepath.Base(path) != "go.mod" {
			continue
		}
		nestedModules[filepath.ToSlash(filepath.Dir(path))+"/"] = struct{}{}
	}

	files := make([]modzip.File, 0, len(paths))
	for _, path := range paths {
		if path == "" || nestedModulePath(path, nestedModules) {
			continue
		}
		info, err := os.Lstat(filepath.Join(repoDir, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("release: stat tracked file %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		files = append(files, trackedModuleFile{repoDir: repoDir, path: path, info: info})
	}
	return files, nil
}

func gitTrackedPaths(output string) []string {
	if output == "" {
		return nil
	}
	parts := strings.Split(output, "\x00")
	paths := parts[:0]
	for _, path := range parts {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func nestedModulePath(path string, nestedModules map[string]struct{}) bool {
	for directory := range nestedModules {
		if strings.HasPrefix(path, directory) {
			return true
		}
	}
	return false
}

type trackedModuleFile struct {
	repoDir string
	path    string
	info    os.FileInfo
}

func (f trackedModuleFile) Path() string {
	return f.path
}

func (f trackedModuleFile) Lstat() (os.FileInfo, error) {
	return f.info, nil
}

func (f trackedModuleFile) Open() (io.ReadCloser, error) {
	return os.Open(filepath.Join(f.repoDir, filepath.FromSlash(f.path)))
}

type restoreSelfReplaceJob struct {
	sparkwing.Base
	RepoDir string
}

func (job *restoreSelfReplaceJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).DryRun(job.dryRun).Risk("destructive")
	return nil, nil
}

func (job *restoreSelfReplaceJob) run(ctx context.Context) error {
	return restoreSelfReplaceIn(ctx, job.RepoDir)
}

func restoreSelfReplaceIn(ctx context.Context, repoDir string) error {
	path := filepath.Join(repoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	newBody, changed := restoreSelfReplace(string(body))
	if !changed {
		sparkwing.Info(ctx, ".sparkwing/go.mod self-replace already present; skipping")
		return nil
	}
	// #nosec G703 -- a pipeline job writing under the repository it runs in
	if err := os.WriteFile(path, []byte(newBody), 0o644); err != nil {
		return fmt.Errorf("release: write .sparkwing/go.mod: %w", err)
	}
	if result, err := sparkwing.Exec(ctx, "go", "mod", "tidy").Dir(filepath.Join(repoDir, ".sparkwing")).Run(); err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("release: tidy restored .sparkwing module: %s", detail)
	}
	if err := regenerateScaffoldAPISnapshot(ctx, repoDir); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	addArgs := append([]string{"add", "--"}, sparkwingPinArtifacts...)
	if _, err := runGitIn(ctx, repoDir, addArgs...); err != nil {
		return fmt.Errorf("release: git add release-version artifacts: %w", err)
	}
	if _, err := runGitIn(ctx, repoDir, "commit", "-m",
		"chore: restore .sparkwing/ local self-replace for next dev cycle"); err != nil {
		return fmt.Errorf("release: git commit .sparkwing module files: %w", err)
	}
	branch, err := currentBranch(ctx, repoDir)
	if err != nil {
		return fmt.Errorf("release: detect branch for restore push: %w", err)
	}
	if _, err := runGitIn(ctx, repoDir, "push", "origin", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("release: push restore commit: %w", err)
	}
	sparkwing.Info(ctx, "restored .sparkwing/ self-replace + pushed to %s", branch)
	return nil
}

func (job *restoreSelfReplaceJob) dryRun(ctx context.Context) error {
	path := filepath.Join(job.RepoDir, ".sparkwing", "go.mod")
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

func stripSelfReplace(body, version string) (string, bool, error) {
	requireRe := regexp.MustCompile(`(?m)^([\t ]*(?:require[\t ]+)?)` + regexp.QuoteMeta(sparkwingModulePath) + `[\t ]+v[0-9][0-9A-Za-z.+-]*[\t ]*$`)
	if !requireRe.MatchString(body) {
		return "", false, fmt.Errorf(".sparkwing/go.mod: no `%s vX.Y.Z` require line found", sparkwingModulePath)
	}
	newBody := requireRe.ReplaceAllString(body, "${1}"+sparkwingModulePath+" "+version)

	replaceRe := regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(sparkwingModulePath) + `\s*=>\s*\.\.\s*$`)
	location := replaceRe.FindStringIndex(newBody)
	if location == nil {
		return newBody, newBody != body, nil
	}
	start := location[0]
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
	end := location[1]
	if end < len(newBody) && newBody[end] == '\n' {
		end++
	}
	newBody = newBody[:start] + newBody[end:]
	return newBody, true, nil
}

func restoreSelfReplace(body string) (string, bool) {
	replaceRe := regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(sparkwingModulePath) + `\s*=>\s*\.\.\s*$`)
	if replaceRe.MatchString(body) {
		return body, false
	}
	trimmed := strings.TrimRight(body, "\n")
	return trimmed + "\n\n" + selfReplaceComment + selfReplaceLine + "\n", true
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
			"CHANGELOG.md has both [Unreleased] (%d entries) and [%s] (%d entries) populated -- "+
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
	location := re.FindStringIndex(body)
	if location == nil {
		return "", fmt.Errorf("CHANGELOG.md has no [Unreleased] heading to rewrite")
	}
	newHeader := "## [Unreleased]\n\n## [" + version + "] - " + date
	return body[:location[0]] + newHeader + body[location[1]:], nil
}

func versionEntries(body, version string) (int, error) {
	target := strings.TrimSpace(version)
	if target == "" {
		return 0, fmt.Errorf("empty version")
	}
	lines := strings.Split(body, "\n")
	inSection := false
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
				inSection = true
				continue
			}
			if inSection {
				break
			}
			continue
		}
		if !inSection {
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
	inSection := false
	count := 0
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "## ") {
			heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			heading = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(heading, "["), "]"))
			if strings.EqualFold(heading, "Unreleased") {
				inSection = true
				continue
			}
			if inSection {
				break
			}
			continue
		}
		if !inSection {
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

func (job *pushTagJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).
		DryRun(job.dryRun).
		Risk("destructive", "prod")
	return nil, nil
}

func (job *pushTagJob) run(ctx context.Context) error {
	version := job.Version.Get(ctx)
	exists, err := tagExistsOnRemote(ctx, job.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: re-check remote tags: %w", err)
	}
	if exists {
		return fmt.Errorf("release: tag %s appeared on origin between validate and push (race); abort", version)
	}
	branch, err := currentBranch(ctx, job.RepoDir)
	if err != nil {
		return fmt.Errorf("release: detect current branch: %w", err)
	}
	if branch != "main" {
		sparkwing.Info(ctx, "release: tagging from branch %q", branch)
	}
	if err := ensureBranchContainsRemote(ctx, job.RepoDir, branch); err != nil {
		return err
	}
	if _, err := runGitIn(ctx, job.RepoDir, "push", "origin", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("release: push branch: %w", err)
	}
	if _, err := runGitIn(ctx, job.RepoDir, "tag", "-a", version, "-m", "Release "+version); err != nil {
		return fmt.Errorf("release: create tag: %w", err)
	}
	if _, err := runGitIn(ctx, job.RepoDir, "push", "origin", "refs/tags/"+version); err != nil {
		return fmt.Errorf("release: push tag: %w", err)
	}
	sparkwing.Info(ctx, "pushed %s + branch %s to origin (GitHub Actions release.yaml will take over)", version, branch)
	return nil
}

func (job *pushTagJob) dryRun(ctx context.Context) error {
	version := job.Version.Get(ctx)
	branch, err := currentBranch(ctx, job.RepoDir)
	if err != nil {
		sparkwing.Info(ctx, "dry-run: would tag %s and push branch+tag to origin (current-branch lookup failed: %v)", version, err)
		return nil
	}
	sparkwing.Info(ctx, "dry-run: would push branch %s + tag %s to origin", branch, version)
	return nil
}

func currentBranch(ctx context.Context, repoDir string) (string, error) {
	output, err := runGitIn(ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
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

type checkReleaseLineageJob struct {
	sparkwing.Base
	RepoDir string
}

func (job *checkReleaseLineageJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).SafeWithoutDryRun()
	return nil, nil
}

func (job *checkReleaseLineageJob) run(ctx context.Context) error {
	return ensureLineageContainsLatestRelease(ctx, job.RepoDir)
}

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
	var executionError *sparkwing.ExecError
	if errors.As(err, &executionError) && executionError.ExitCode == 1 {
		return fmt.Errorf("release: latest release %s is absent from HEAD history; inspect `git fetch --tags origin && git log %s --not HEAD`, then merge the missing release history before releasing",
			latest, latest)
	}
	return fmt.Errorf("release: lineage check for %s: %w", latest, err)
}

func validateReleaseVersion(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return errors.New("release: --version is required (e.g. --version v0.6.1)")
	}
	if !strings.HasPrefix(version, "v") {
		return fmt.Errorf("release: version %q must begin with 'v' (e.g. v0.6.1)", version)
	}
	if !semver.IsValid(version) {
		return fmt.Errorf("release: version %q is not valid semver (expected vX.Y.Z)", version)
	}
	if semver.Prerelease(version) != "" || semver.Build(version) != "" {
		return fmt.Errorf("release: version %q includes pre-release / build metadata; release pipeline only cuts stable tags", version)
	}
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != 3 {
		return fmt.Errorf("release: version %q must be vX.Y.Z", version)
	}
	if semver.Major(version) != "v0" {
		return fmt.Errorf("release: version %q is v1.0.0+ but sparkwing is locked to v0.x. "+
			"Bumping to v1+ commits the public API surface (see VERSIONING.md); "+
			"if that's intentional, remove the pre-1.0 lock in .sparkwing/jobs/release.go and resubmit", version)
	}
	return nil
}

func tagExistsOnRemote(ctx context.Context, repoDir, tag string) (bool, error) {
	output, err := runGitIn(ctx, repoDir, "ls-remote", "--tags", "origin", "refs/tags/"+tag)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) != "", nil
}

func runGitIn(ctx context.Context, directory string, arguments ...string) (string, error) {
	result, err := sparkwing.Exec(ctx, "git", arguments...).Dir(directory).Run()
	if err != nil {
		message := strings.TrimSpace(result.Stderr)
		if message == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, message)
	}
	return result.Stdout, nil
}

func runGitRawIn(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(string(exitErr.Stderr))
			if message != "" {
				return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), err, message)
			}
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(arguments, " "), err)
	}
	return output, nil
}

const releaseTagCeiling = "v1.0.0"

func highestReleaseTag(tags []string) string {
	var best string
	for _, tag := range tags {
		if !semver.IsValid(tag) {
			continue
		}
		if semver.Prerelease(tag) != "" || semver.Build(tag) != "" {
			continue
		}
		if semver.Compare(tag, releaseTagCeiling) >= 0 {
			continue
		}
		if best == "" || semver.Compare(tag, best) > 0 {
			best = tag
		}
	}
	return best
}

func latestSemverTagIn(ctx context.Context, repoDir string) (string, error) {
	output, err := runGitIn(ctx, repoDir, "ls-remote", "--tags", "origin")
	if err != nil {
		return "", err
	}
	var tags []string
	for _, line := range strings.Split(output, "\n") {
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

func bumpVersion(version, kind string) (string, error) {
	if !semver.IsValid(version) {
		return "", fmt.Errorf("not semver: %s", version)
	}
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not vX.Y.Z: %s", version)
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

// SAFETY: An absent requirement registry differs from a malformed registry, which fails the gate.
var errNoRequirementRegistry = errors.New("no `var migrationRequirements = map[int][]string{...}`")

// SAFETY: Source parsing also reads the registry at released tags.
func parseMigrationRequirements(source string) (map[int][]string, error) {
	location := migrationRequirementsDeclRe.FindStringIndex(source)
	if location == nil {
		return nil, fmt.Errorf("%w in %s", errNoRequirementRegistry, storeSchemaSourcePath)
	}
	block := migrationRequirementsBlockRe.FindStringSubmatch(source[location[0]:])
	if block == nil {
		return nil, fmt.Errorf("unterminated migrationRequirements registry in %s", storeSchemaSourcePath)
	}
	output := map[int][]string{}
	for _, entry := range migrationRequirementsEntryRe.FindAllStringSubmatch(block[1], -1) {
		version, err := strconv.Atoi(entry[1])
		if err != nil {
			return nil, fmt.Errorf("parse %s requirement version %q: %w", storeSchemaSourcePath, entry[1], err)
		}
		for _, name := range migrationRequirementNameRe.FindAllStringSubmatch(entry[2], -1) {
			output[version] = append(output[version], name[1])
		}
	}
	return output, nil
}

func requirementsAddedBetween(registry map[int][]string, previousSchema, currentSchema int) []string {
	var added []string
	for v := previousSchema + 1; v <= currentSchema; v++ {
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
	match := storeSchemaConstRe.FindStringSubmatch(goSource)
	if match == nil {
		return 0, fmt.Errorf("no `const expectedSchemaVersion = N` in %s", storeSchemaSourcePath)
	}
	version, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, fmt.Errorf("parse %s schema version %q: %w", storeSchemaSourcePath, match[1], err)
	}
	return version, nil
}

type checkSchemaBreakJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (job *checkSchemaBreakJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).SafeWithoutDryRun()
	return nil, nil
}

func (job *checkSchemaBreakJob) run(ctx context.Context) error {
	version := job.Version.Get(ctx)
	previousTag, err := latestSemverTagIn(ctx, job.RepoDir)
	if err != nil {
		return fmt.Errorf("release: resolve previous tag for schema gate: %w", err)
	}
	if previousTag == "" {
		sparkwing.Info(ctx, "no previous release tag; skipping schema-break changelog gate")
		return nil
	}
	currentSource, err := os.ReadFile(filepath.Join(job.RepoDir, filepath.FromSlash(storeSchemaSourcePath)))
	if err != nil {
		return fmt.Errorf("release: read %s: %w", storeSchemaSourcePath, err)
	}
	currentSchema, err := parseStoreSchemaVersion(string(currentSource))
	if err != nil {
		return fmt.Errorf("release: current schema: %w", err)
	}
	previousSource, err := runGitIn(ctx, job.RepoDir, "show", previousTag+":"+storeSchemaSourcePath)
	if err != nil {
		return fmt.Errorf("release: read %s at %s: %w", storeSchemaSourcePath, previousTag, err)
	}
	previousSchema, err := parseStoreSchemaVersion(previousSource)
	if err != nil {
		return fmt.Errorf("release: schema at %s: %w", previousTag, err)
	}
	currentRegistry, err := parseMigrationRequirements(string(currentSource))
	if err != nil {
		return fmt.Errorf("release: current schema requirements: %w", err)
	}
	added, err := requirementsAdded(previousSource, currentRegistry, previousSchema, currentSchema)
	if err != nil {
		return fmt.Errorf("release: schema requirements at %s: %w", previousTag, err)
	}
	if previousSchema == currentSchema && len(added) == 0 {
		sparkwing.Info(ctx, "runs-store schema unchanged since %s (schema %d) and no requirement added; gate passes", previousTag, currentSchema)
		return nil
	}
	body, err := os.ReadFile(filepath.Join(job.RepoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	issues := LintSchemaBreak(string(body), version, previousSchema, currentSchema, added)
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
			previousSchema, currentSchema, strings.Join(added, ", "))
		return nil
	}
	sparkwing.Info(ctx, "runs-store schema %d -> %d adds no requirement and carries a store changelog entry; gate passes", previousSchema, currentSchema)
	return nil
}

// SAFETY: Without a prior registry, only newly added schema versions require classification.
func requirementsAdded(previousSource string, currentRegistry map[int][]string, previousSchema, currentSchema int) ([]string, error) {
	previousRegistry, err := parseMigrationRequirements(previousSource)
	if errors.Is(err, errNoRequirementRegistry) {
		return requirementsAddedBetween(currentRegistry, previousSchema, currentSchema), nil
	}
	if err != nil {
		return nil, err
	}
	return requirementsAddedSince(previousRegistry, currentRegistry), nil
}

type checkWireBreakJob struct {
	sparkwing.Base
	RepoDir string
	Version sparkwing.Ref[string]
}

func (job *checkWireBreakJob) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(work, "run", job.run).SafeWithoutDryRun()
	return nil, nil
}

func (job *checkWireBreakJob) run(ctx context.Context) error {
	version := job.Version.Get(ctx)
	previousTag, err := latestSemverTagIn(ctx, job.RepoDir)
	if err != nil {
		return fmt.Errorf("release: resolve previous tag for wire gate: %w", err)
	}
	if previousTag == "" {
		sparkwing.Info(ctx, "no previous release tag; skipping wire-surface changelog gate")
		return nil
	}
	cuts, err := wireCutsSince(ctx, job.RepoDir, previousTag)
	if err != nil {
		return fmt.Errorf("release: diff the wire surface against %s: %w", previousTag, err)
	}
	if len(cuts) == 0 {
		sparkwing.Info(ctx, "wire surface added to or unchanged since %s; gate passes", previousTag)
		return nil
	}
	body, err := os.ReadFile(filepath.Join(job.RepoDir, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("release: read CHANGELOG.md: %w", err)
	}
	issues := LintWireBreak(string(body), version, cuts, migrationsFS(job.RepoDir))
	if len(issues) > 0 {
		var b strings.Builder
		for _, i := range issues {
			b.WriteString(i.Format())
			b.WriteByte('\n')
		}
		return fmt.Errorf("release: undeclared wire-surface cut blocks %s:\n%s", version, b.String())
	}
	sparkwing.Info(ctx, "wire surface cuts %s since %s and the changelog declares it; gate passes",
		strings.Join(describeCuts(cuts), ", "), previousTag)
	return nil
}

func wireCutsSince(ctx context.Context, repoDir, previousTag string) ([]wireCut, error) {
	states := make([]wireSurfaceState, 0, len(wireSurfaces))
	for _, surface := range wireSurfaces {
		prev, present, err := fileAtTag(ctx, repoDir, previousTag, surface.path)
		if err != nil {
			return nil, err
		}
		if !present {
			sparkwing.Info(ctx, "%s does not exist at %s; nothing to diff", surface.path, previousTag)
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
