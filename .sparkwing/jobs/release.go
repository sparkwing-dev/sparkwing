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
	return "Runs a contract preflight (the embedded documentation mirror, and the documentation, help, and environment-variable contracts) before the pre-commit, pre-push and template-verify gates, validates the release shape (clean tree, free tag, non-empty CHANGELOG.md [Unreleased] section), commits the CHANGELOG [Unreleased] rename, then pushes the branch and a vX.Y.Z tag to origin. Afterwards it pins .sparkwing/go.mod and pkg/scaffold to the released version, regenerates the public API snapshots, and restores the dogfood self-replace, in two further commits, and pushes the branch again. The .github/workflows/release.yaml workflow takes over from the tag push to build cross-platform binaries (uploaded to GH Releases) and multi-arch container images (published to GHCR). This pipeline never builds or publishes artifacts itself."
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

	gateContracts := sparkwing.Job(plan, "gate-contracts", &checkContractsJob{RepoDir: repoDir})
	gateContracts.Needs(clean)

	gatePreCommit := sparkwing.Job(plan, "gate-pre-commit", &PreCommit{})
	gatePreCommit.Needs(clean, gateContracts)

	gatePrePush := sparkwing.Job(plan, "gate-pre-push", func(ctx context.Context) error {
		return (&PrePush{AllowReleaseLineSelfReplace: true}).run(ctx)
	})
	gatePrePush.Needs(clean, gatePreCommit)

	gateTemplates := sparkwing.Job(plan, "gate-template-verify", func(ctx context.Context) error {
		_, err := sparkwing.RunAndAwait[TemplateVerifySummary, sparkwing.NoInputs](
			ctx, "template-verify", "summary",
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

	pushTag := sparkwing.Job(plan, "push-tag", &pushTagJob{
		Version: versionRef,
		RepoDir: repoDir,
	})
	pushTag.Needs(validate, clean, changelog, schemaGate, gateTemplates, gateLineage)

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
	exists, err := tagExistsOnRemote(ctx, j.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: check remote tags: %w", err)
	}
	if exists {
		return fmt.Errorf("release: tag %s already exists on origin (never force-push a module tag; increment to a new version)", version)
	}
	if err := j.checkHostingReleaseConstant(ctx, version); err != nil {
		return err
	}
	sparkwing.Info(ctx, "version %s is free on origin", version)
	return nil
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
	if err := writeChangelogPair(j.RepoDir, action.newBody); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if _, err := runGitIn(ctx, j.RepoDir, "add", "CHANGELOG.md", embeddedChangelogRel); err != nil {
		return fmt.Errorf("release: git add changelog: %w", err)
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

const selfReplaceComment = `// The pipelines tree is consumed as the same module path the SDK
// itself ships, so the require above is a placeholder; this replace
// pins it to the parent checkout (the sparkwing repo root). The
// pattern follows the standard "consumer .sparkwing/ uses a local
// replace during development" convention; here the parent IS the
// SDK rather than a sibling.
`

const selfReplaceLine = "replace github.com/sparkwing-dev/sparkwing => .."

const sparkwingModulePath = "github.com/sparkwing-dev/sparkwing"

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
	return bumpSelfReplace(ctx, j.RepoDir, j.Version.Get(ctx))
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
		sparkwing.Info(ctx, "release-version artifacts already in shipped shape; skipping")
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

func (j *prepareSelfReplaceJob) dryRun(ctx context.Context) error {
	version := j.Version.Get(ctx)
	path := filepath.Join(j.RepoDir, ".sparkwing", "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("release: read .sparkwing/go.mod: %w", err)
	}
	_, moduleChanged, err := stripSelfReplace(string(body), version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	aligned, err := releaseVersionArtifactsAligned(j.RepoDir, version)
	if err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if !moduleChanged && aligned {
		sparkwing.Info(ctx, "dry-run: release-version artifacts already in shipped shape; no rewrite")
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := selfModuleZipFiles(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := modzip.Create(&out, module.Version{Path: sparkwingModulePath, Version: version}, files); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func selfModuleZipFiles(ctx context.Context, repoDir string) ([]modzip.File, error) {
	out, err := runGitRawIn(ctx, repoDir, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("release: list tracked files: %w", err)
	}
	paths := gitTrackedPaths(string(out))
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

func gitTrackedPaths(out string) []string {
	if out == "" {
		return nil
	}
	parts := strings.Split(out, "\x00")
	paths := parts[:0]
	for _, path := range parts {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func nestedModulePath(path string, nestedModules map[string]struct{}) bool {
	for dir := range nestedModules {
		if strings.HasPrefix(path, dir) {
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

func (j *restoreSelfReplaceJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	sparkwing.Step(w, "run", j.run).DryRun(j.dryRun).Risk("destructive")
	return nil, nil
}

func (j *restoreSelfReplaceJob) run(ctx context.Context) error {
	return restoreSelfReplaceIn(ctx, j.RepoDir)
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

func runGitRawIn(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if msg != "" {
				return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
			}
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

const releaseTagCeiling = "v1.0.0"

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

const storeSchemaSourcePath = "pkg/store/store.go"

var storeSchemaConstRe = regexp.MustCompile(`(?m)^const\s+expectedSchemaVersion\s*=\s*(\d+)\b`)

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
