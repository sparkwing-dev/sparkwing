package orchestrator

import (
	"os/exec"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func DetectGit(dir string) *sparkwing.Git {
	g := &sparkwing.Git{}
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		return cmd.Output()
	}
	if out, err := git("rev-parse", "HEAD"); err == nil {
		g.SHA = strings.TrimSpace(string(out))
	}
	if out, err := git("rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch := strings.TrimSpace(string(out))
		if branch != "HEAD" {
			g.Branch = branch
		}
	}
	if out, err := git("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		name := strings.TrimSpace(string(out))
		g.DefaultBranch = strings.TrimPrefix(name, "origin/")
	}
	if out, err := git("remote", "get-url", "origin"); err == nil {
		g.RepoURL = strings.TrimSpace(string(out))
		owner, repo := parseGithubURL(g.RepoURL)
		if owner != "" && repo != "" {
			g.Repo = owner + "/" + repo
		}
	}
	return g
}

func parseGithubURL(url string) (owner, repo string) {
	url = strings.TrimSuffix(url, ".git")
	var path string
	switch {
	case strings.HasPrefix(url, "git@github.com:"):
		path = strings.TrimPrefix(url, "git@github.com:")
	case strings.HasPrefix(url, "https://github.com/"):
		path = strings.TrimPrefix(url, "https://github.com/")
	case strings.HasPrefix(url, "ssh://git@github.com/"):
		path = strings.TrimPrefix(url, "ssh://git@github.com/")
	default:
		return "", ""
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
