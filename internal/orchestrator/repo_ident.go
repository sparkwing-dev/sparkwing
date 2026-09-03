package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func repoShortName(dir string) string {
	d := filepath.Clean(dir)
	for {
		gitPath := filepath.Join(d, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if !info.IsDir() {
				return gitFileRepoName(gitPath, d)
			}
			if name := originRepoName(gitPath); name != "" {
				return name
			}
			if name := alternatesRepoName(gitPath); name != "" {
				return name
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

func originRepoName(gitDir string) string {
	cmd := exec.Command("git", "config", "--file", filepath.Join(gitDir, "config"), "--includes", "--get", "remote.origin.url")
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	return repoNameFromURL(strings.TrimSpace(string(raw)))
}

func repoNameFromURL(remote string) string {
	return store.RepoIdentityFromURL(remote)
}

func localRepoName(repoPath string) string {
	return store.RepoIdentityFromPath(repoPath)
}

func alternatesRepoName(gitDir string) string {
	raw, err := os.ReadFile(filepath.Join(gitDir, "objects", "info", "alternates"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		objects := strings.TrimSpace(line)
		if objects == "" || strings.HasPrefix(objects, "#") {
			continue
		}
		if !filepath.IsAbs(objects) {
			objects = filepath.Join(gitDir, "objects", objects)
		}
		repo := filepath.Dir(filepath.Clean(objects))
		if name := originRepoName(repo); name != "" {
			return name
		}
		if name := localRepoName(repo); name != "" {
			return name
		}
	}
	return ""
}

func gitFileRepoName(gitFile, dir string) string {
	gitDir := gitDirPointer(gitFile, dir)
	if gitDir == "" {
		return filepath.Base(dir)
	}
	if filepath.Base(filepath.Dir(gitDir)) == "worktrees" {
		common := filepath.Dir(filepath.Dir(gitDir))
		if name := originRepoName(common); name != "" {
			return name
		}
		if name := alternatesRepoName(common); name != "" {
			return name
		}
		if filepath.Base(common) == ".git" {
			return filepath.Base(filepath.Dir(common))
		}
		return strings.TrimSuffix(filepath.Base(common), ".git")
	}
	if name := originRepoName(gitDir); name != "" {
		return name
	}
	if name := alternatesRepoName(gitDir); name != "" {
		return name
	}
	return filepath.Base(dir)
}

func gitDirPointer(gitFile, dir string) string {
	raw, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !ok {
			continue
		}
		gitDir := strings.TrimSpace(rest)
		if gitDir == "" {
			return ""
		}
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(dir, gitDir)
		}
		return filepath.Clean(gitDir)
	}
	return ""
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
	return store.JoinProfileKey(repo, pipeline)
}

func currentProfileKey(pipeline string) string {
	return scopedProfileKey(currentRepoShortName(), pipeline)
}
