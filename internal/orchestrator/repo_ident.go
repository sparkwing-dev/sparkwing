package orchestrator

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func repoShortName(dir string) string {
	d := filepath.Clean(dir)
	for {
		gitPath := filepath.Join(d, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if !info.IsDir() {
				if main := worktreeRepoDir(gitPath, d); main != "" {
					return filepath.Base(main)
				}
			}
			return filepath.Base(d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

func worktreeRepoDir(gitFile, worktreeDir string) string {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	gitDir := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok {
			gitDir = strings.TrimSpace(rest)
			break
		}
	}
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreeDir, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return ""
	}
	common := filepath.Dir(filepath.Dir(gitDir))
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return strings.TrimSuffix(common, ".git")
}

func currentRepoShortName() string {
	if workDir := sparkwing.CurrentRuntime().WorkDir; workDir != "" {
		return repoShortName(workDir)
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return repoShortName(wd)
}

func scopedProfileKey(repo, pipeline string) string {
	if repo == "" || pipeline == "" {
		return pipeline
	}
	return repo + "/" + pipeline
}

func currentProfileKey(pipeline string) string {
	return scopedProfileKey(currentRepoShortName(), pipeline)
}
