// Package git is the sparkwing SDK's repo-inspection helper layer:
// commit SHA, branch, dirty-tree detection, deterministic fileset
// hash, tag listing, and safe tag push.
//
// Every function takes an explicit `repoDir` parameter. Callers
// operating on the run's working tree usually go through
// sparkwing.RunContext.Git / sparkwing.Runtime().Git; release
// pipelines that operate on sibling clones import this package
// directly.
//
// All helpers are free functions with (T, error) returns; nothing
// panics. Shell-outs are context-aware so cancellation and timeouts
// propagate.
//
// Leaf package: must not import sparkwing/ proper.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

// ErrTagAlreadyExists is returned by PushTag when the tag already
// exists on the remote. Go-module safety: never force-push a tag.
// Callers wanting to move a tag must delete it on the remote first.
var ErrTagAlreadyExists = errors.New("git: tag already exists on remote")

// ShortCommit returns the HEAD commit SHA in repoDir truncated to 12
// characters. Errors when repoDir is not a git working tree.
func ShortCommit(ctx context.Context, repoDir string) (string, error) {
	sha, err := CurrentSHA(ctx, repoDir)
	if err != nil {
		return "", err
	}
	if len(sha) > 12 {
		return sha[:12], nil
	}
	return sha, nil
}

// CurrentSHA returns the full HEAD commit SHA in repoDir. Errors
// loudly when not in a git repo.
func CurrentSHA(ctx context.Context, repoDir string) (string, error) {
	out, err := runGit(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranch returns the branch name HEAD points at in repoDir.
// Detached HEAD returns ("", nil) -- a normal state during CI builds
// that check out a specific SHA. Errors loudly when not in a git
// repo.
func CurrentBranch(ctx context.Context, repoDir string) (string, error) {
	out, err := runGit(ctx, repoDir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// symbolic-ref --quiet exits 1 on detached HEAD with no
		// output; rev-parse confirms we're still in a git repo.
		if _, sErr := runGit(ctx, repoDir, "rev-parse", "--git-dir"); sErr != nil {
			return "", sErr
		}
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// RemoteOriginURL returns the URL of `origin` in repoDir, or "" with
// nil error when no origin remote is configured. Errors only when
// repoDir isn't a git tree.
func RemoteOriginURL(ctx context.Context, repoDir string) (string, error) {
	out, err := runGit(ctx, repoDir, "remote", "get-url", "origin")
	if err != nil {
		// Distinguish "no origin" from "not a repo".
		if _, sErr := runGit(ctx, repoDir, "rev-parse", "--git-dir"); sErr != nil {
			return "", sErr
		}
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// IsDirty reports whether the working tree in repoDir has uncommitted
// changes -- either unstaged or staged-but-not-committed. Errors
// loudly when not in a git repo.
func IsDirty(ctx context.Context, repoDir string) (bool, error) {
	out, err := runGit(ctx, repoDir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// FilesetHash returns a deterministic 12-char hex hash derived from
// the contents of every file that would land in a Docker build
// context for repoDir. With git, hashes tracked + untracked-not-
// ignored files; without, falls back to a filesystem walk with a
// small skip list. .dockerignore is honored in both modes.
//
// File contents are read from disk (not from the git blob) so
// staged-but-not-committed changes are reflected in the hash --
// content addressing keys off the tree the build will actually see.
func FilesetHash(ctx context.Context, repoDir string) (string, error) {
	files := listGitFiles(ctx, repoDir)
	if files == nil {
		files = listFilesystemFiles(repoDir)
	}
	if len(files) == 0 {
		return "", nil
	}

	ignore := loadDockerignore(repoDir)
	seen := map[string]bool{}
	keep := files[:0]
	for _, f := range files {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		if matchesIgnore(f, ignore) {
			continue
		}
		keep = append(keep, f)
	}
	sort.Strings(keep)

	h := sha256.New()
	base := repoDir
	if base == "" {
		base = "."
	}
	for _, f := range keep {
		data, err := os.ReadFile(filepath.Join(base, f))
		if err != nil {
			continue
		}
		h.Write([]byte(f))
		h.Write([]byte{0})
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:12], nil
}

// listGitFiles returns tracked + untracked-not-ignored files in
// repoDir, or nil when repoDir is not a git tree.
func listGitFiles(ctx context.Context, repoDir string) []string {
	gitDir := ".git"
	if repoDir != "" {
		gitDir = filepath.Join(repoDir, ".git")
	}
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		return nil
	}
	tracked, err := runGit(ctx, repoDir, "ls-files")
	if err != nil {
		return nil
	}
	untracked, err := runGit(ctx, repoDir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	return splitLines(tracked + "\n" + untracked)
}

// listFilesystemFiles walks repoDir applying a small skip list for
// directories that are never part of a build context.
func listFilesystemFiles(repoDir string) []string {
	root := repoDir
	if root == "" {
		root = "."
	}
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		"dist": true, "build": true, ".sparkwing": true,
	}
	var files []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files
}

func loadDockerignore(repoDir string) []string {
	path := ".dockerignore"
	if repoDir != "" {
		path = filepath.Join(repoDir, ".dockerignore")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			patterns = append(patterns, line)
		}
	}
	return patterns
}

func matchesIgnore(path string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(path, p) || strings.HasPrefix(path+"/", p) {
				return true
			}
		}
		if matched, _ := filepath.Match(p, filepath.Base(path)); matched {
			return true
		}
		if strings.HasPrefix(path, p+"/") || path == p {
			return true
		}
	}
	return false
}

// ChangedFiles returns paths (repo-relative) modified between `since`
// and HEAD in repoDir. `since` may be any git revision (commit SHA,
// branch name, tag, "HEAD~5"). Empty `since` returns the working
// tree's currently-modified set (`git diff --name-only HEAD`).
func ChangedFiles(ctx context.Context, repoDir, since string) ([]string, error) {
	args := []string{"diff", "--name-only"}
	if since == "" {
		args = append(args, "HEAD")
	} else {
		args = append(args, since+"...HEAD")
	}
	out, err := runGit(ctx, repoDir, args...)
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// TagsAtHead returns every tag pointing at HEAD in repoDir. Order is
// whatever git returns (lexical by default).
func TagsAtHead(ctx context.Context, repoDir string) ([]string, error) {
	out, err := runGit(ctx, repoDir, "tag", "--points-at", "HEAD")
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

// LatestTag returns the highest semver tag in repoDir matching the
// given prefix, or "" if none. Ordering is by semver: v0.10.0 >
// v0.2.0. Non-semver tags are skipped. Prefix is matched literally
// and preserved in the returned tag; pass "" to consider every tag.
func LatestTag(ctx context.Context, repoDir, prefix string) (string, error) {
	out, err := runGit(ctx, repoDir, "tag", "--list")
	if err != nil {
		return "", err
	}
	tags := splitLines(out)

	var best string
	var bestSem string
	for _, t := range tags {
		if prefix != "" && !strings.HasPrefix(t, prefix) {
			continue
		}
		rest := strings.TrimPrefix(t, prefix)
		sem := rest
		if !strings.HasPrefix(sem, "v") {
			sem = "v" + sem
		}
		if !semver.IsValid(sem) {
			continue
		}
		if best == "" || semver.Compare(sem, bestSem) > 0 {
			best = t
			bestSem = sem
		}
	}
	return best, nil
}

// TagExistsOnRemote checks whether `tag` exists on origin in repoDir.
// Returns (true, nil) if present, (false, nil) if absent, (_, err)
// on transport failure.
func TagExistsOnRemote(ctx context.Context, repoDir, tag string) (bool, error) {
	if tag == "" {
		return false, errors.New("git: empty tag")
	}
	out, err := runGit(ctx, repoDir, "ls-remote", "--tags", "origin", "refs/tags/"+tag)
	if err != nil {
		return false, fmt.Errorf("git: ls-remote origin: %w", err)
	}
	return strings.TrimSpace(out) != "", nil
}

// PushTag creates an annotated tag locally in repoDir and pushes it
// to origin. Refuses with ErrTagAlreadyExists if the remote already
// has that tag (Go-module safety: never force-push a tag).
func PushTag(ctx context.Context, repoDir, tag, message string) error {
	if tag == "" {
		return errors.New("git: empty tag")
	}

	exists, err := TagExistsOnRemote(ctx, repoDir, tag)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("%w: %s", ErrTagAlreadyExists, tag)
	}

	if message == "" {
		message = tag
	}
	if _, err := runGit(ctx, repoDir, "tag", "-a", tag, "-m", message); err != nil {
		return fmt.Errorf("git: create tag %s: %w", tag, err)
	}
	if _, err := runGit(ctx, repoDir, "push", "origin", "refs/tags/"+tag); err != nil {
		return fmt.Errorf("git: push tag %s: %w", tag, err)
	}
	return nil
}

// runGit runs `git <args...>` in repoDir and returns stdout. Errors
// include stderr. Empty repoDir runs in process CWD.
func runGit(ctx context.Context, repoDir string, args ...string) (string, error) {
	// All public ctx-taking git helpers funnel through this point,
	// so a single guard here covers them all.
	planguard.Guard(ctx, "git "+firstWord(args))
	cmd := exec.CommandContext(ctx, "git", args...)
	if repoDir != "" {
		cmd.Dir = repoDir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}

// firstWord returns the first arg, used as the helper name in
// planguard panic messages.
func firstWord(args []string) string {
	if len(args) == 0 {
		return "git command"
	}
	return args[0]
}

func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
