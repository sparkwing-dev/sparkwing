// Package githooks resolves the hook directory git actually reads for a
// checkout, and reports when a core.hooksPath override leaves the hooks
// sparkwing installed unread.
//
// core.hooksPath replaces .git/hooks wholesale. Set in the global config it
// applies to every repository on the machine, so hooks written into
// .git/hooks are silently never run and a commit or push gate disappears
// without a message. Detect names that condition; the install path repairs
// it by claiming core.hooksPath for the repository and chaining the global
// hooks from there, so both layers keep firing.
package githooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Marker identifies hook files sparkwing manages. A script containing it is
// sparkwing's to rewrite or remove; anything else belongs to the operator.
const Marker = "Installed by sparkwing"

// pipelineCall is the line a managed hook uses to run a pipeline. Its
// presence separates a gate from a forwarder that only chains onward.
const pipelineCall = "sparkwing run "

// Git runs a git subcommand inside dir (or the process working directory
// when dir is empty) and returns stdout. Callers inject it so hook
// detection can be exercised against a fake git configuration.
type Git func(dir string, args ...string) (string, error)

// Shadow is a checkout whose sparkwing gates git will never run, because
// core.hooksPath points at a different directory.
type Shadow struct {
	// Repo is the checkout root the gates belong to.
	Repo string `json:"repo"`
	// HooksDir is the directory the gates are installed in.
	HooksDir string `json:"hooks_dir"`
	// ActiveDir is the directory git reads hooks from instead.
	ActiveDir string `json:"active_dir"`
	// Scope is the git config scope that set core.hooksPath, either
	// "local" or "global".
	Scope string `json:"scope"`
	// Gates are the shadowed hook names that run a pipeline.
	Gates []string `json:"gates"`
}

// Summary states in one line what is not running and where git is looking
// instead.
func (s Shadow) Summary() string {
	return fmt.Sprintf("git reads hooks from %s, not %s, so the sparkwing hooks installed there never fire: %s",
		s.ActiveDir, s.HooksDir, strings.Join(s.Gates, ", "))
}

// Remedy is the instruction that ends the shadowing. It differs by scope: a
// machine-wide override is something an install can claim away, while an
// override the repository set itself was deliberate and is cleared by hand.
func (s Shadow) Remedy() string {
	if s.Scope == "local" {
		return fmt.Sprintf("this repo sets its own core.hooksPath; clear it with `git -C %s config --unset core.hooksPath`, then run `sparkwing pipeline hooks install`", s.Repo)
	}
	return fmt.Sprintf("run `sparkwing pipeline hooks install` in %s -- it points this repo at its own hooks and chains the global ones so both still fire", s.Repo)
}

// Dir returns the directory git keeps repoRoot's hooks in. Hooks live in
// the git common directory, which a linked worktree reaches through its
// .git file, so the answer is not always repoRoot/.git/hooks.
func Dir(repoRoot string) (string, error) {
	gitPath := filepath.Join(repoRoot, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", fmt.Errorf("no git checkout at %s: %w", repoRoot, err)
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "hooks"), nil
	}
	gitDir, err := linkedGitDir(gitPath, repoRoot)
	if err != nil {
		return "", err
	}
	common := gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common = resolveAgainst(gitDir, strings.TrimSpace(string(data)))
	}
	return filepath.Join(common, "hooks"), nil
}

// linkedGitDir reads the git directory a linked worktree's .git file names.
func linkedGitDir(gitFile, repoRoot string) (string, error) {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok {
			return resolveAgainst(repoRoot, strings.TrimSpace(rest)), nil
		}
	}
	return "", fmt.Errorf("%s names no gitdir", gitFile)
}

func resolveAgainst(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// GlobalPath returns the directory a global core.hooksPath points at, or ""
// when the machine sets none.
func GlobalPath(git Git) string {
	return configPath(git, "", "--global")
}

// LocalPath returns repoRoot's own core.hooksPath override, or "" when the
// repository sets none.
func LocalPath(git Git, repoRoot string) string {
	return configPath(git, repoRoot, "--local")
}

// ActivePath returns the directory git reads repoRoot's hooks from and the
// config scope that set it. A repository override wins over the machine's,
// matching git's own precedence. Two empty strings mean no override is set,
// so git reads the repository's own hook directory.
func ActivePath(git Git, repoRoot string) (dir, scope string) {
	if v := LocalPath(git, repoRoot); v != "" {
		return v, "local"
	}
	if v := GlobalPath(git); v != "" {
		return v, "global"
	}
	return "", ""
}

func configPath(git Git, dir, scope string) string {
	out, err := git(dir, "config", scope, "--type=path", "core.hooksPath")
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(out)
	if v == "" {
		return ""
	}
	if !filepath.IsAbs(v) && dir != "" {
		v = filepath.Join(dir, v)
	}
	return filepath.Clean(v)
}

// Gates returns the sorted names of sparkwing-managed hooks in hooksDir
// that run a pipeline. Forwarders sparkwing writes to preserve other hooks
// are managed too, but they gate nothing, so they are not listed.
func Gates(hooksDir string) []string {
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(hooksDir, e.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(body), Marker) && strings.Contains(string(body), pipelineCall) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// Detect reports the sparkwing gates installed in repoRoot that git will not
// run, because core.hooksPath points somewhere else. It returns nil when the
// repository installs no gate, and when git already reads the directory the
// gates live in.
func Detect(git Git, repoRoot string) (*Shadow, error) {
	hooksDir, err := Dir(repoRoot)
	if err != nil {
		return nil, err
	}
	gates := Gates(hooksDir)
	if len(gates) == 0 {
		return nil, nil
	}
	active, scope := ActivePath(git, repoRoot)
	if active == "" || SameDir(active, hooksDir) {
		return nil, nil
	}
	return &Shadow{
		Repo:      repoRoot,
		HooksDir:  hooksDir,
		ActiveDir: active,
		Scope:     scope,
		Gates:     gates,
	}, nil
}

// SameDir reports whether two paths name the same directory, resolving
// symlinks so that two spellings of one path still match.
func SameDir(a, b string) bool {
	return canon(a) == canon(b)
}

func canon(p string) string {
	c := filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(c); err == nil {
		return r
	}
	return c
}
