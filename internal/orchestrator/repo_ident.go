package orchestrator

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
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
				return filepath.Base(d)
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

// originRepoName reads the repository name from a checkout's origin remote, so two
// clones of one repository resolve to one name. Git resolves config includes and
// case-insensitive keys according to the same rules used by the checkout.
//
// A checkout carrying no origin returns "", and the caller keeps the directory name:
// a repository with no remote has no identity beyond where it sits.
func originRepoName(gitDir string) string {
	cmd := exec.Command("git", "config", "--file", filepath.Join(gitDir, "config"), "--includes", "--get", "remote.origin.url")
	raw, err := cmd.Output()
	if err != nil {
		return ""
	}
	return repoNameFromURL(strings.TrimSpace(string(raw)))
}

// repoNameFromURL normalizes a remote to host/owner/path without credentials,
// scheme, a trailing slash, or a .git suffix. The namespace keeps repositories
// with the same basename from sharing capacity measurements.
func repoNameFromURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.HasPrefix(remote, "/") || (len(remote) >= 3 && remote[1] == ':' && (remote[2] == '/' || remote[2] == '\\')) {
		return localRepoName(remote)
	}
	if !strings.Contains(remote, "://") {
		if colon := strings.Index(remote, ":"); colon >= 0 && strings.Contains(remote[:colon], "@") {
			remote = "ssh://" + remote[:colon] + "/" + remote[colon+1:]
		}
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "file" {
		return localRepoName(parsed.Host + parsed.Path)
	}
	if parsed.Host == "" {
		return ""
	}
	host := parsed.Host
	if parsed.User != nil {
		host = strings.TrimPrefix(host, parsed.User.String()+"@")
	}
	path := strings.TrimSuffix(strings.Trim(strings.TrimSpace(parsed.Path), "/"), ".git")
	if path == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + path
}

func localRepoName(repoPath string) string {
	normalized := path.Clean(strings.ReplaceAll(strings.TrimSpace(repoPath), "\\", "/"))
	normalized = strings.TrimSuffix(normalized, ".git")
	if normalized == "" || normalized == "." {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("local:%x", sum[:12])
}

// alternatesRepoName names a checkout that borrows its objects from another
// repository instead of carrying a remote. A thin clone written for one run has no
// origin, so its object store is the only thing tying it to the repository it holds:
// .git/objects/info/alternates points at that repository's objects directory, whose
// parent is the repository itself.
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
	return strconv.Itoa(len(repo)) + ":" + repo + pipeline
}

func currentProfileKey(pipeline string) string {
	return scopedProfileKey(currentRepoShortName(), pipeline)
}
