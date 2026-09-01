package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

const sdkModulePath = "github.com/sparkwing-dev/sparkwing"

const scaffoldFallbackRel = "pkg/scaffold/version.go"

const scaffoldAPISnapshotRel = ".apidiff/pkg_scaffold.txt"

const kubernetesE2EPipelineModuleRel = "testdata/k8s-e2e/repo/.sparkwing"

var scaffoldFallbackVersionRe = regexp.MustCompile(`FallbackSDKVersion = "(v[^"]*)"`)

var sparkwingPinArtifacts = []string{
	scaffoldFallbackRel,
	".sparkwing/go.mod",
	".sparkwing/go.sum",
	scaffoldAPISnapshotRel,
	kubernetesE2EPipelineModuleRel + "/go.mod",
	kubernetesE2EPipelineModuleRel + "/go.sum",
}

type VersionFreshnessOptions struct {
	AllowReleaseLineSelfReplace bool
}

func CheckVersionsFreshness(ctx context.Context, repoRoot string) error {
	return CheckVersionsFreshnessWithOptions(ctx, repoRoot, VersionFreshnessOptions{})
}

func CheckVersionsFreshnessWithOptions(ctx context.Context, repoRoot string, options VersionFreshnessOptions) error {
	mods, err := findGoModFiles(repoRoot)
	if err != nil {
		return fmt.Errorf("scan go.mod files: %w", err)
	}
	var problems []string
	for _, modPath := range mods {
		bs, err := os.ReadFile(modPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", modPath, err)
		}
		f, err := modfile.Parse(modPath, bs, nil)
		if err != nil {
			return fmt.Errorf("parse %s: %w", modPath, err)
		}
		relMod, _ := filepath.Rel(repoRoot, modPath)
		for _, req := range f.Require {
			if !isWatchedModule(req.Mod.Path) {
				continue
			}
			if replace := findReplaceFor(f, req.Mod.Path); replace != nil {
				if !isLocalReplace(replace) {
					if msg := checkAgainstLatest(ctx, replace.New.Path, replace.New.Version, modPath); msg != "" {
						problems = append(problems, fmt.Sprintf("%s: %s", relMod, msg))
					}
					continue
				}
				localPath, err := resolveLocalReplacePath(replace.New.Path, modPath)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: replace -> %s: %v", relMod, replace.New.Path, err))
					continue
				}
				if !shouldCheckLocalReplaceFreshness(relMod, req.Mod.Path, localPath, repoRoot, options) {
					continue
				}
				behind, behindBy, err := localBehindRemote(ctx, localPath)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s: replace -> %s: %v", relMod, localPath, err))
					continue
				}
				if behind {
					problems = append(problems, fmt.Sprintf(
						"%s: %s replace -> %s is %d commits behind origin/main (pull or stop iterating)",
						relMod, req.Mod.Path, localPath, behindBy,
					))
				}
			} else {
				if msg := checkAgainstLatest(ctx, req.Mod.Path, req.Mod.Version, modPath); msg != "" {
					problems = append(problems, fmt.Sprintf("%s: %s", relMod, msg))
				}
			}
		}
	}
	if msg := checkScaffoldFallbackPin(ctx, repoRoot); msg != "" {
		problems = append(problems, msg)
	}
	if len(problems) > 0 {
		return fmt.Errorf("version freshness:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func shouldCheckLocalReplaceFreshness(relMod, modulePath, localPath, repoRoot string, options VersionFreshnessOptions) bool {
	if !options.AllowReleaseLineSelfReplace {
		return true
	}
	if relMod != ".sparkwing/go.mod" || modulePath != sdkModulePath {
		return true
	}
	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return true
	}
	absLocalPath, err := filepath.Abs(localPath)
	if err != nil {
		return true
	}
	return filepath.Clean(absLocalPath) != filepath.Clean(absRepoRoot)
}

func checkScaffoldFallbackPin(ctx context.Context, repoRoot string) string {
	latest, err := latestReleasedTag(ctx, repoRoot, majorCapFor(sdkModulePath))
	if err != nil {
		return fmt.Sprintf("scaffold fallback pin: cannot resolve latest %s release (%v)", sdkModulePath, err)
	}
	pinned, err := readFallbackSDKVersionFile(repoRoot)
	if err != nil {
		return fmt.Sprintf("scaffold fallback pin: %v", err)
	}
	return scaffoldFallbackProblem(pinned, latest)
}

func latestReleasedTag(ctx context.Context, repoRoot string, cap int) (string, error) {
	out, err := captureGit(ctx, repoRoot, "tag", "--list", "v*")
	if err != nil {
		return "", fmt.Errorf("git tag: %w", err)
	}
	var stable []string
	for _, line := range strings.Split(out, "\n") {
		v := strings.TrimSpace(line)
		if !semver.IsValid(v) || semver.Prerelease(v) != "" {
			continue
		}
		if cap >= 0 {
			if maj, ok := semverMajor(v); !ok || maj > cap {
				continue
			}
		}
		stable = append(stable, v)
	}
	if len(stable) == 0 {
		return "", fmt.Errorf("no stable release tags in %s within cap", repoRoot)
	}
	semver.Sort(stable)
	return stable[len(stable)-1], nil
}

func scaffoldFallbackProblem(pinned, latest string) string {
	if !semver.IsValid(pinned) {
		return fmt.Sprintf(
			"scaffold fallback pin %q is not a valid release version (set scaffold.FallbackSDKVersion to a published tag)",
			pinned,
		)
	}
	if semver.Compare(pinned, latest) < 0 {
		return fmt.Sprintf(
			"scaffold fallback pin %s is behind latest release %s (bump scaffold.FallbackSDKVersion to %s so fresh source-built scaffolds build green)",
			pinned, latest, latest,
		)
	}
	return ""
}

var watchedModulePrefixes = []string{
	"github.com/sparkwing-dev/sparkwing",
	"github.com/sparkwing-dev/sparks-core",
}

var maxAllowedMajor = map[string]int{
	"github.com/sparkwing-dev/sparkwing": 0,
}

func isWatchedModule(path string) bool {
	for _, p := range watchedModulePrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

func majorCapFor(modulePath string) int {
	if cap, ok := maxAllowedMajor[modulePath]; ok {
		return cap
	}
	return -1
}

func findGoModFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if name == "go.mod" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func findReplaceFor(f *modfile.File, modulePath string) *modfile.Replace {
	for _, r := range f.Replace {
		if r.Old.Path == modulePath {
			return r
		}
	}
	return nil
}

func isLocalReplace(r *modfile.Replace) bool {
	p := r.New.Path
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/")
}

func resolveLocalReplacePath(target, modPath string) (string, error) {
	dir := filepath.Dir(modPath)
	abs, err := filepath.Abs(filepath.Join(dir, target))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}
	return abs, nil
}

func localBehindRemote(ctx context.Context, localPath string) (bool, int, error) {
	if _, err := os.Stat(filepath.Join(localPath, ".git")); err != nil {
		return false, 0, nil
	}
	_ = runGit(ctx, localPath, "fetch", "--quiet", "origin", "main")
	if err := runGit(ctx, localPath, "rev-parse", "--verify", "--quiet", "origin/main"); err != nil {
		return false, 0, nil
	}
	out, err := captureGit(ctx, localPath, "rev-list", "--count", "HEAD..origin/main")
	if err != nil {
		return false, 0, fmt.Errorf("rev-list HEAD..origin/main: %w", err)
	}
	n := 0
	if s := strings.TrimSpace(out); s != "" {
		_, _ = fmt.Sscanf(s, "%d", &n)
	}
	return n > 0, n, nil
}

func checkAgainstLatest(ctx context.Context, modulePath, pinned, fromModFile string) string {
	if pinned == "" {
		return ""
	}
	cap := majorCapFor(modulePath)
	// safety: v1.0.0+ tags were pushed by mistake and can't be removed from the proxy cache.
	if cap >= 0 {
		if pinnedMajor, ok := semverMajor(pinned); ok && pinnedMajor > cap {
			return fmt.Sprintf(
				"%s pinned at %s but is capped at major v%d (the README states this module stays below v%d; v%d+ tags on the proxy were pushed by mistake)",
				modulePath, pinned, cap, cap+1, cap+1,
			)
		}
	}
	latest, err := latestReleasedVersion(ctx, modulePath, fromModFile)
	if err != nil {
		return fmt.Sprintf("%s: cannot resolve latest version (%v)", modulePath, err)
	}
	if semver.Compare(pinned, latest) >= 0 {
		return ""
	}
	return fmt.Sprintf("%s pinned at %s but %s is available (run `go get %s@%s`)",
		modulePath, pinned, latest, modulePath, latest)
}

func semverMajor(v string) (int, bool) {
	if !semver.IsValid(v) {
		return 0, false
	}
	maj := semver.Major(v)
	if !strings.HasPrefix(maj, "v") {
		return 0, false
	}
	n := 0
	if _, err := fmt.Sscanf(maj[1:], "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}

func latestReleasedVersion(ctx context.Context, modulePath, fromModFile string) (string, error) {
	dir := filepath.Dir(fromModFile)
	out, err := captureCmd(ctx, dir, "go", "list", "-m", "-versions", modulePath)
	if err != nil {
		return "", err
	}
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) < 2 {
		return "", fmt.Errorf("no versions reported for %s", modulePath)
	}
	cap := majorCapFor(modulePath)
	var stable []string
	for _, v := range parts[1:] {
		if !semver.IsValid(v) || semver.Prerelease(v) != "" {
			continue
		}
		if cap >= 0 {
			if maj, ok := semverMajor(v); !ok || maj > cap {
				continue
			}
		}
		stable = append(stable, v)
	}
	if len(stable) == 0 {
		return "", fmt.Errorf("no stable releases for %s within cap", modulePath)
	}
	semver.Sort(stable)
	return stable[len(stable)-1], nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func captureGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

func captureCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func autoBumpSparkwingPinIfStale(ctx context.Context, repoRoot string) (_ string, retErr error) {
	latest, err := latestReleasedTag(ctx, repoRoot, majorCapFor(sdkModulePath))
	if err != nil {
		return "", fmt.Errorf("resolve latest sparkwing release: %w", err)
	}
	pinned, aligned, err := coherentReleaseVersionArtifacts(repoRoot)
	if err != nil {
		return "", err
	}
	if aligned && semver.Compare(pinned, latest) >= 0 {
		return "", nil
	}
	if err := requireCleanSparkwingPinArtifacts(ctx, repoRoot); err != nil {
		return "", err
	}
	defer func() {
		if retErr != nil {
			if err := restoreSparkwingPinArtifacts(context.WithoutCancel(ctx), repoRoot); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("restore sparkwing pin artifacts: %w", err))
			}
		}
	}()

	if err := bumpFallbackSDKVersionFile(repoRoot, latest); err != nil {
		return "", fmt.Errorf("bump FallbackSDKVersion: %w", err)
	}
	if _, err := captureCmd(ctx, repoRoot, "go", "-C", ".sparkwing", "mod", "edit",
		"-require", sdkModulePath+"@"+latest); err != nil {
		return "", fmt.Errorf("go mod edit .sparkwing/go.mod: %w", err)
	}
	if _, err := captureCmd(ctx, repoRoot, "go", "-C", ".sparkwing", "mod", "tidy"); err != nil {
		return "", fmt.Errorf("go mod tidy .sparkwing: %w", err)
	}
	if err := bumpPipelineModulePin(ctx, repoRoot, kubernetesE2EPipelineModuleRel, latest); err != nil {
		return "", err
	}
	if err := regenerateScaffoldAPISnapshot(ctx, repoRoot); err != nil {
		return "", err
	}
	if err := commitSparkwingPinBump(ctx, repoRoot, latest); err != nil {
		return "", fmt.Errorf("commit sparkwing pin bump: %w", err)
	}
	return latest, nil
}

func bumpPipelineModulePin(ctx context.Context, repoRoot, moduleDir, version string) error {
	if _, err := captureCmd(ctx, repoRoot, "go", "-C", moduleDir, "mod", "edit",
		"-require", sdkModulePath+"@"+version); err != nil {
		return fmt.Errorf("go mod edit %s/go.mod: %w", moduleDir, err)
	}
	if _, err := captureCmd(ctx, repoRoot, "go", "-C", moduleDir, "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy %s: %w", moduleDir, err)
	}
	return nil
}

func readPipelineModulePin(repoRoot, moduleDir string) (string, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(moduleDir), "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	parsed, err := modfile.Parse(path, body, nil)
	if err != nil {
		return "", err
	}
	for _, requirement := range parsed.Require {
		if requirement.Mod.Path == sdkModulePath {
			return requirement.Mod.Version, nil
		}
	}
	return "", fmt.Errorf("%s has no %s requirement", path, sdkModulePath)
}

func releaseVersionArtifactsAligned(repoRoot, version string) (bool, error) {
	pinned, aligned, err := coherentReleaseVersionArtifacts(repoRoot)
	if err != nil {
		return false, err
	}
	return aligned && pinned == version, nil
}

func coherentReleaseVersionArtifacts(repoRoot string) (string, bool, error) {
	fallback, err := readFallbackSDKVersionFile(repoRoot)
	if err != nil {
		return "", false, err
	}
	snapshot, err := readScaffoldVersionArtifact(repoRoot, scaffoldAPISnapshotRel)
	if err != nil {
		return "", false, err
	}
	pipeline, err := readPipelineModulePin(repoRoot, ".sparkwing")
	if err != nil {
		return "", false, err
	}
	fixture, err := readPipelineModulePin(repoRoot, kubernetesE2EPipelineModuleRel)
	if err != nil {
		return "", false, err
	}
	return fallback, fallback == snapshot && fallback == pipeline && fallback == fixture, nil
}

func regenerateScaffoldAPISnapshot(ctx context.Context, repoRoot string) error {
	tmp, err := os.MkdirTemp("", "sparkwing-apidiff-")
	if err != nil {
		return fmt.Errorf("create API snapshot temp directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	cmd := exec.CommandContext(ctx, "bash", "bin/regen-api-snapshot.sh", tmp)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("regenerate API snapshots: %w\n%s", err, bytes.TrimSpace(out))
	}
	snapshot, err := os.ReadFile(filepath.Join(tmp, filepath.Base(scaffoldAPISnapshotRel)))
	if err != nil {
		return fmt.Errorf("read regenerated scaffold API snapshot: %w", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, filepath.FromSlash(scaffoldAPISnapshotRel)), snapshot, 0o644); err != nil {
		return fmt.Errorf("write scaffold API snapshot: %w", err)
	}
	return nil
}

func requireCleanSparkwingPinArtifacts(ctx context.Context, repoRoot string) error {
	for _, path := range sparkwingPinArtifacts {
		if err := exec.CommandContext(ctx, "git", "-C", repoRoot, "ls-files", "--error-unmatch", "--", path).Run(); err != nil {
			return fmt.Errorf("sparkwing pin artifact %s is not tracked", path)
		}
	}
	checks := [][]string{
		append([]string{"-C", repoRoot, "diff", "--quiet", "--"}, sparkwingPinArtifacts...),
		{"-C", repoRoot, "diff", "--cached", "--quiet", "--"},
	}
	for _, args := range checks {
		if err := exec.CommandContext(ctx, "git", args...).Run(); err != nil {
			return fmt.Errorf("sparkwing pin artifacts have uncommitted changes")
		}
	}
	return nil
}

func restoreSparkwingPinArtifacts(ctx context.Context, repoRoot string) error {
	args := append([]string{"-C", repoRoot, "restore", "--source=HEAD", "--staged", "--worktree", "--"}, sparkwingPinArtifacts...)
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git restore: %w\n%s", err, bytes.TrimSpace(out))
	}
	return nil
}

func bumpFallbackSDKVersionFile(repoRoot, version string) error {
	path := filepath.Join(repoRoot, filepath.FromSlash(scaffoldFallbackRel))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !scaffoldFallbackVersionRe.Match(data) {
		return fmt.Errorf("FallbackSDKVersion pattern not found in %s", path)
	}
	updated := scaffoldFallbackVersionRe.ReplaceAllLiteral(data, []byte(`FallbackSDKVersion = "`+version+`"`))
	return os.WriteFile(path, updated, 0o644)
}

func readFallbackSDKVersionFile(repoRoot string) (string, error) {
	return readScaffoldVersionArtifact(repoRoot, scaffoldFallbackRel)
}

func readScaffoldVersionArtifact(repoRoot, rel string) (string, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	match := scaffoldFallbackVersionRe.FindSubmatch(data)
	if match == nil {
		return "", fmt.Errorf("FallbackSDKVersion pattern not found in %s", path)
	}
	return string(match[1]), nil
}

func commitSparkwingPinBump(ctx context.Context, repoRoot, version string) error {
	addArgs := []string{
		"-C", repoRoot, "add", "--",
	}
	addArgs = append(addArgs, sparkwingPinArtifacts...)
	if out, err := exec.CommandContext(ctx, "git", addArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w\n%s", err, bytes.TrimSpace(out))
	}
	msg := "chore: bump sparkwing pin to " + version
	if out, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "commit", "-m", msg).CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, bytes.TrimSpace(out))
	}
	return nil
}
