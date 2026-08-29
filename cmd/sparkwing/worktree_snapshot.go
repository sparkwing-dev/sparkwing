package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

const maxWorktreeSnapshotBytes int64 = 500 << 20

type worktreeSnapshot struct {
	RepoRoot   string
	BaseSHA    string
	SHA        string
	BundlePath string
	FileCount  int
	Size       int64
	tempDir    string
}

func (s *worktreeSnapshot) close() error {
	if s == nil || s.tempDir == "" {
		return nil
	}
	dir := s.tempDir
	s.tempDir = ""
	return os.RemoveAll(dir)
}

func captureWorktreeSnapshot(ctx context.Context, start string) (*worktreeSnapshot, error) {
	repoRoot, err := gitOutput(ctx, start, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("working-tree snapshot requires a Git checkout: %w", err)
	}
	repoRoot = strings.TrimSpace(repoRoot)
	baseSHA, err := gitOutput(ctx, repoRoot, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("working-tree snapshot requires an existing HEAD commit: %w", err)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	objectFormat, err := gitOutput(ctx, repoRoot, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("inspect Git object format: %w", err)
	}
	if strings.TrimSpace(objectFormat) != "sha1" {
		return nil, fmt.Errorf("working-tree snapshot supports SHA-1 repositories only; found %s", strings.TrimSpace(objectFormat))
	}
	shallow, err := gitOutput(ctx, repoRoot, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, fmt.Errorf("inspect shallow checkout: %w", err)
	}
	if strings.TrimSpace(shallow) == "true" {
		return nil, fmt.Errorf("working-tree snapshot requires a complete repository; deepen this shallow checkout first")
	}

	if unmerged, uerr := gitOutput(ctx, repoRoot, nil, "ls-files", "-u"); uerr != nil {
		return nil, fmt.Errorf("inspect index conflicts: %w", uerr)
	} else if strings.TrimSpace(unmerged) != "" {
		return nil, fmt.Errorf("working-tree snapshot refuses an unmerged index; resolve conflicts first")
	}
	if sparse, _ := gitOutput(ctx, repoRoot, nil, "config", "--bool", "core.sparseCheckout"); strings.TrimSpace(sparse) == "true" {
		return nil, fmt.Errorf("working-tree snapshot does not support sparse checkouts")
	}
	if err := rejectWorktreeFilters(ctx, repoRoot); err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "sparkwing-worktree-snapshot-*")
	if err != nil {
		return nil, fmt.Errorf("create snapshot workspace: %w", err)
	}
	snapshot := &worktreeSnapshot{RepoRoot: repoRoot, BaseSHA: baseSHA, tempDir: tempDir}
	fail := func(err error) (*worktreeSnapshot, error) {
		_ = snapshot.close()
		return nil, err
	}

	gitDir := filepath.Join(tempDir, "repo.git")
	if out, ierr := exec.CommandContext(ctx, "git", "init", "--bare", "--quiet", gitDir).CombinedOutput(); ierr != nil {
		return fail(fmt.Errorf("initialize snapshot object store: %w: %s", ierr, strings.TrimSpace(string(out))))
	}
	commonDir, err := gitOutput(ctx, repoRoot, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fail(fmt.Errorf("resolve Git object store: %w", err))
	}
	commonDir = strings.TrimSpace(commonDir)
	alternates := filepath.Join(gitDir, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(commonDir, "objects")+"\n"), 0o600); err != nil {
		return fail(fmt.Errorf("configure snapshot object store: %w", err))
	}

	indexPath := filepath.Join(tempDir, "index")
	objectEnv := []string{
		"GIT_INDEX_FILE=" + indexPath,
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(gitDir, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(commonDir, "objects"),
	}

	var tree string
	for attempt := 0; attempt < 3; attempt++ {
		first, terr := captureSnapshotTree(ctx, repoRoot, baseSHA, indexPath, objectEnv)
		if terr != nil {
			return fail(terr)
		}
		second, terr := captureSnapshotTree(ctx, repoRoot, baseSHA, indexPath, objectEnv)
		if terr != nil {
			return fail(terr)
		}
		if first == second {
			tree = first
			break
		}
	}
	if tree == "" {
		return fail(fmt.Errorf("working tree changed while Sparkwing captured it; retry after writes settle"))
	}

	if err := rejectGitlinks(ctx, gitDir, tree); err != nil {
		return fail(err)
	}
	sha, err := commitSnapshotTree(ctx, repoRoot, tree, baseSHA, objectEnv)
	if err != nil {
		return fail(err)
	}
	ref := bincache.SeedRef(sha)
	if _, err := gitDirOutput(ctx, gitDir, "update-ref", ref, sha); err != nil {
		return fail(fmt.Errorf("name snapshot ref: %w", err))
	}
	bundlePath := filepath.Join(tempDir, "snapshot.bundle")
	if _, err := gitDirOutput(ctx, gitDir, "bundle", "create", bundlePath, ref); err != nil {
		return fail(fmt.Errorf("create snapshot bundle: %w", err))
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return fail(fmt.Errorf("inspect snapshot bundle: %w", err))
	}
	if info.Size() > maxWorktreeSnapshotBytes {
		return fail(fmt.Errorf("working-tree snapshot bundle is %d bytes; limit is %d bytes", info.Size(), maxWorktreeSnapshotBytes))
	}
	files, err := gitDirOutput(ctx, gitDir, "ls-tree", "-r", "--name-only", "-z", sha)
	if err != nil {
		return fail(fmt.Errorf("count snapshot files: %w", err))
	}

	snapshot.SHA = sha
	snapshot.BundlePath = bundlePath
	snapshot.Size = info.Size()
	snapshot.FileCount = bytes.Count([]byte(files), []byte{0})
	return snapshot, nil
}

func captureSnapshotTree(ctx context.Context, repoRoot, baseSHA, indexPath string, env []string) (string, error) {
	if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("reset snapshot index: %w", err)
	}
	if _, err := gitOutput(ctx, repoRoot, env, "read-tree", baseSHA); err != nil {
		return "", fmt.Errorf("seed snapshot index: %w", err)
	}
	if _, err := gitOutput(ctx, repoRoot, env, "-c", "core.safecrlf=false", "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("capture working tree: %w", err)
	}
	if err := restoreRawSnapshotBlobs(ctx, repoRoot, env); err != nil {
		return "", err
	}
	tree, err := gitOutput(ctx, repoRoot, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write snapshot tree: %w", err)
	}
	return strings.TrimSpace(tree), nil
}

type snapshotIndexEntry struct {
	mode string
	sha  string
	path string
}

func restoreRawSnapshotBlobs(ctx context.Context, repoRoot string, env []string) error {
	out, err := gitOutput(ctx, repoRoot, env, "ls-files", "--stage", "-z")
	if err != nil {
		return fmt.Errorf("inspect snapshot index: %w", err)
	}
	var entries []snapshotIndexEntry
	for _, raw := range bytes.Split([]byte(out), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		metadata, path, ok := bytes.Cut(raw, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !ok || len(fields) != 3 || string(fields[2]) != "0" {
			return fmt.Errorf("inspect snapshot index: malformed stage entry")
		}
		info, statErr := os.Lstat(filepath.Join(repoRoot, string(path)))
		if statErr != nil {
			return fmt.Errorf("inspect snapshot file %q: %w", path, statErr)
		}
		if info.Mode().IsRegular() {
			entries = append(entries, snapshotIndexEntry{mode: string(fields[0]), sha: string(fields[1]), path: string(path)})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || string(fields[0]) == "160000" {
			continue
		}
		return fmt.Errorf("working-tree snapshot does not support non-regular file %q", path)
	}

	var updates bytes.Buffer
	for start := 0; start < len(entries); start += 256 {
		end := min(start+256, len(entries))
		args := []string{"hash-object", "-w", "--no-filters", "--"}
		for _, entry := range entries[start:end] {
			args = append(args, entry.path)
		}
		hashes, hashErr := gitOutput(ctx, repoRoot, env, args...)
		if hashErr != nil {
			return fmt.Errorf("hash raw snapshot files: %w", hashErr)
		}
		fields := strings.Fields(hashes)
		if len(fields) != end-start {
			return fmt.Errorf("hash raw snapshot files: got %d object ids for %d paths", len(fields), end-start)
		}
		for i, entry := range entries[start:end] {
			if fields[i] == entry.sha {
				continue
			}
			fmt.Fprintf(&updates, "%s %s 0\t%s%c", entry.mode, fields[i], entry.path, byte(0))
		}
	}
	if updates.Len() == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "update-index", "-z", "--index-info")
	cmd.Env = appendGitEnv(env)
	cmd.Stdin = &updates
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore raw snapshot files: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func commitSnapshotTree(ctx context.Context, repoRoot, tree, parent string, env []string) (string, error) {
	identity := []string{
		"GIT_AUTHOR_NAME=Sparkwing",
		"GIT_AUTHOR_EMAIL=workspace@sparkwing.dev",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Sparkwing",
		"GIT_COMMITTER_EMAIL=workspace@sparkwing.dev",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "commit.gpgsign=false", "-C", repoRoot, "commit-tree", tree, "-p", parent)
	cmd.Env = appendGitEnv(append(env, identity...))
	cmd.Stdin = strings.NewReader("sparkwing working-tree snapshot\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create snapshot commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func rejectGitlinks(ctx context.Context, gitDir, tree string) error {
	out, err := gitDirOutput(ctx, gitDir, "ls-tree", "-r", tree)
	if err != nil {
		return fmt.Errorf("inspect snapshot tree: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "160000 ") {
			return fmt.Errorf("working-tree snapshot does not support submodules or embedded Git repositories")
		}
	}
	return nil
}

func rejectWorktreeFilters(ctx context.Context, repoRoot string) error {
	out, err := gitOutput(ctx, repoRoot, nil, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return fmt.Errorf("list snapshot files: %w", err)
	}
	if out == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "check-attr", "-z", "--stdin", "filter", "working-tree-encoding")
	cmd.Stdin = strings.NewReader(out)
	checked, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect Git content filters: %w", err)
	}
	parts := bytes.Split(checked, []byte{0})
	for i := 0; i+2 < len(parts); i += 3 {
		value := string(parts[i+2])
		if value == "" || value == "unspecified" || value == "unset" {
			continue
		}
		attribute := string(parts[i+1])
		if attribute == "filter" {
			return fmt.Errorf("working-tree snapshot refuses Git content filter %q on %q; remote materialization would not guarantee the same bytes", value, string(parts[i]))
		}
		return fmt.Errorf("working-tree snapshot refuses Git %s=%q on %q; remote materialization would not guarantee the same bytes", attribute, value, string(parts[i]))
	}
	return nil
}

func gitOutput(ctx context.Context, repoRoot string, env []string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repoRoot}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Env = appendGitEnv(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func gitDirOutput(ctx context.Context, gitDir string, args ...string) (string, error) {
	fullArgs := append([]string{"--git-dir", gitDir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func appendGitEnv(overrides []string) []string {
	env := append([]string(nil), os.Environ()...)
	for _, override := range overrides {
		key, _, ok := strings.Cut(override, "=")
		if !ok {
			continue
		}
		prefix := key + "="
		filtered := env[:0]
		for _, current := range env {
			if !strings.HasPrefix(current, prefix) {
				filtered = append(filtered, current)
			}
		}
		env = append(filtered, override)
	}
	return env
}

func snapshotBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := unit, 0
	for value := n / unit; value >= unit && exp < 5; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
