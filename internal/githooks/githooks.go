package githooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const Marker = "Installed by sparkwing"

const pipelineCall = "sparkwing run "

type Git func(dir string, args ...string) (string, error)

type Shadow struct {
	Repo string `json:"repo"`

	HooksDir string `json:"hooks_dir"`

	ActiveDir string `json:"active_dir"`

	Scope string `json:"scope"`

	Gates []string `json:"gates"`
}

func (s Shadow) Summary() string {
	return fmt.Sprintf("git reads hooks from %s, not %s, so the sparkwing hooks installed there never fire: %s",
		s.ActiveDir, s.HooksDir, strings.Join(s.Gates, ", "))
}

func (s Shadow) Remedy() string {
	if s.Scope == "local" {
		return fmt.Sprintf("this repo sets its own core.hooksPath; clear it with `git -C %s config --unset core.hooksPath`, then run `sparkwing pipeline hooks install`", s.Repo)
	}
	return fmt.Sprintf("run `sparkwing pipeline hooks install` in %s -- it points this repo at its own hooks and chains the global ones so both still fire", s.Repo)
}

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

func GlobalPath(git Git) string {
	return configPath(git, "", "--global")
}

func LocalPath(git Git, repoRoot string) string {
	return configPath(git, repoRoot, "--local")
}

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
