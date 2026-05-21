package jobs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// CheckVersionsFreshness verifies every sparkwing-ecosystem dependency
// in every go.mod under repoRoot is current:
//
//   - Direct require (no replace): the pinned version must be >= the
//     latest released tag.
//   - Replace -> local path: the local checkout must not be behind its
//     origin/main.
//
// Returns nil when everything is current. Returns a non-nil error
// listing every problem when anything is behind.
//
// Watched module prefixes are listed in watchedModulePrefixes. Add
// more there as the ecosystem grows.
func CheckVersionsFreshness(ctx context.Context, repoRoot string) error {
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
				// Replace -> local path: check the local checkout is
				// not behind its origin/main.
				if !isLocalReplace(replace) {
					// Replace to a different module/version on the
					// proxy: treat like a normal pin for the purpose
					// of the freshness check.
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
				// Direct pin: check pin against latest released tag.
				if msg := checkAgainstLatest(ctx, req.Mod.Path, req.Mod.Version, modPath); msg != "" {
					problems = append(problems, fmt.Sprintf("%s: %s", relMod, msg))
				}
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("version freshness:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// watchedModulePrefixes lists every module path whose freshness we
// track. Anything not matching is skipped (third-party deps are out
// of scope; this check only enforces the sparkwing ecosystem stays
// current against itself).
var watchedModulePrefixes = []string{
	"github.com/sparkwing-dev/sparkwing",
	"github.com/sparkwing-dev/sparks-core",
}

func isWatchedModule(path string) bool {
	for _, p := range watchedModulePrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// findGoModFiles returns every go.mod under root, skipping vendored
// trees and the .git tree.
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

// findReplaceFor returns the replace directive matching modulePath if
// any, else nil.
func findReplaceFor(f *modfile.File, modulePath string) *modfile.Replace {
	for _, r := range f.Replace {
		if r.Old.Path == modulePath {
			return r
		}
	}
	return nil
}

// isLocalReplace reports whether the replace target is a filesystem
// path (./... or ../... or absolute) rather than another module.
func isLocalReplace(r *modfile.Replace) bool {
	p := r.New.Path
	return strings.HasPrefix(p, ".") || strings.HasPrefix(p, "/")
}

// resolveLocalReplacePath resolves a replace's filesystem target
// against the directory containing the go.mod that declares it.
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

// localBehindRemote checks whether the git repo at localPath is
// behind its origin/main. Returns (behind, count, error). If there
// is no `origin` remote or no `main` branch, returns (false, 0, nil)
// rather than failing — the local clone may be a personal fork with
// a different default branch, and the freshness check should not
// blow up on that. fetches origin/main first so the comparison is
// against current remote state.
func localBehindRemote(ctx context.Context, localPath string) (bool, int, error) {
	// Bail if not a git repo.
	if _, err := os.Stat(filepath.Join(localPath, ".git")); err != nil {
		return false, 0, nil
	}
	// Best-effort fetch so we don't compare against stale refs.
	_ = runGit(ctx, localPath, "fetch", "--quiet", "origin", "main")
	// Check that origin/main resolves before asking for behind count.
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

// checkAgainstLatest compares pinned against the module's latest
// released tag. Returns an empty string when pinned is current or
// ahead, or a problem description otherwise.
func checkAgainstLatest(ctx context.Context, modulePath, pinned, fromModFile string) string {
	if pinned == "" {
		return ""
	}
	latest, err := latestReleasedVersion(ctx, modulePath, fromModFile)
	if err != nil {
		// Resolution failure is non-fatal — surface the error but
		// don't block. Some modules are still pre-release / private
		// and may not resolve via the proxy in every environment.
		return fmt.Sprintf("%s: cannot resolve latest version (%v)", modulePath, err)
	}
	// modfile pseudo-versions (v0.0.0-YYYYMMDDHHMMSS-sha) for an
	// untagged commit sort below tagged releases, so semver.Compare
	// handles those correctly.
	if semver.Compare(pinned, latest) >= 0 {
		return ""
	}
	return fmt.Sprintf("%s pinned at %s but %s is available (run `go get %s@%s`)",
		modulePath, pinned, latest, modulePath, latest)
}

// latestReleasedVersion uses `go list -m -versions` to discover the
// highest released semver tag for modulePath. The command runs from
// the directory of the consuming go.mod so module-resolution config
// (GOPROXY, GOPRIVATE, replace directives) is respected.
func latestReleasedVersion(ctx context.Context, modulePath, fromModFile string) (string, error) {
	dir := filepath.Dir(fromModFile)
	out, err := captureCmd(ctx, dir, "go", "list", "-m", "-versions", modulePath)
	if err != nil {
		return "", err
	}
	// Output: "<module> v1 v2 v3 ..." — last token is the highest.
	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) < 2 {
		return "", fmt.Errorf("no versions reported for %s", modulePath)
	}
	// Filter to released semver tags (skip pre-release for the
	// "latest" comparison so that a -rc tag doesn't shadow a stable
	// release of the same series).
	var stable []string
	for _, v := range parts[1:] {
		if semver.IsValid(v) && semver.Prerelease(v) == "" {
			stable = append(stable, v)
		}
	}
	if len(stable) == 0 {
		return "", fmt.Errorf("no stable releases for %s", modulePath)
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
