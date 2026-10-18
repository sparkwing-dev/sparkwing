package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/envredact"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const (
	maxWorktreeSnapshotBytes int64 = 500 << 20
	maxWorktreeSnapshotFiles       = 100_000
)

type worktreeSnapshotLimits struct {
	bytes int64
	files int
}

var defaultWorktreeSnapshotLimits = worktreeSnapshotLimits{
	bytes: maxWorktreeSnapshotBytes,
	files: maxWorktreeSnapshotFiles,
}

type worktreeSnapshot struct {
	RepoRoot    string
	BaseSHA     string
	Baseline    bincache.WorkspaceBaseline
	SHA         string
	BundlePath  string
	FileCount   int
	Size        int64
	BundleSize  int64
	tempDir     string
	environment []string
}

func (s *worktreeSnapshot) close() error {
	if s == nil || s.tempDir == "" {
		return nil
	}
	dir := s.tempDir
	s.tempDir = ""
	// #nosec G703 -- dir is the private directory created by MkdirTemp for this snapshot.
	return os.RemoveAll(dir)
}

func captureWorktreeSnapshot(ctx context.Context, env []string, start string, allowedSecretFiles []string) (*worktreeSnapshot, error) {
	return captureWorktreeSnapshotWithLimits(ctx, env, start, defaultWorktreeSnapshotLimits, allowedSecretFiles)
}

func captureWorktreeSnapshotWithLimits(ctx context.Context, env []string, start string, limits worktreeSnapshotLimits, allowedSecretFiles []string) (*worktreeSnapshot, error) {
	return captureSnapshot(ctx, env, start, limits, allowedSecretFiles, nil)
}

func captureSnapshot(ctx context.Context, env []string, start string, limits worktreeSnapshotLimits, allowedSecretFiles []string, selection *burstSourceSelection) (*worktreeSnapshot, error) {
	env, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: env, Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		return nil, err
	}
	repoRoot, err := gitOutput(ctx, start, env, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("working-tree snapshot requires a Git checkout: %w", err)
	}
	repoRoot = strings.TrimSpace(repoRoot)
	baseSHA, err := gitOutput(ctx, repoRoot, env, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("working-tree snapshot requires an existing HEAD commit: %w", err)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	objectFormat, err := gitOutput(ctx, repoRoot, env, "rev-parse", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("inspect Git object format: %w", err)
	}
	if strings.TrimSpace(objectFormat) != "sha1" {
		return nil, fmt.Errorf("working-tree snapshot supports SHA-1 repositories only; found %s", strings.TrimSpace(objectFormat))
	}
	shallow, err := gitOutput(ctx, repoRoot, env, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, fmt.Errorf("inspect shallow checkout: %w", err)
	}
	if strings.TrimSpace(shallow) == "true" {
		return nil, fmt.Errorf("working-tree snapshot requires a complete repository; deepen this shallow checkout first")
	}

	if unmerged, uerr := gitOutput(ctx, repoRoot, env, "ls-files", "-u"); uerr != nil {
		return nil, fmt.Errorf("inspect index conflicts: %w", uerr)
	} else if strings.TrimSpace(unmerged) != "" {
		return nil, fmt.Errorf("working-tree snapshot refuses an unmerged index; resolve conflicts first")
	}
	if sparse, _ := gitOutput(ctx, repoRoot, env, "config", "--bool", "core.sparseCheckout"); strings.TrimSpace(sparse) == "true" {
		return nil, fmt.Errorf("working-tree snapshot does not support sparse checkouts")
	}
	var selectedPaths string
	if selection != nil {
		selectedPaths, err = selection.paths(ctx, env, repoRoot)
		if err != nil {
			return nil, err
		}
	}
	if err := rejectSelectedWorktreeFilters(ctx, env, repoRoot, selectedPaths, selection != nil); err != nil {
		return nil, err
	}

	tempDir, err := os.MkdirTemp("", "sparkwing-worktree-snapshot-*")
	if err != nil {
		return nil, fmt.Errorf("create snapshot workspace: %w", err)
	}
	snapshot := &worktreeSnapshot{
		RepoRoot:    repoRoot,
		BaseSHA:     baseSHA,
		Baseline:    resolveSnapshotBaseline(ctx, env, repoRoot),
		tempDir:     tempDir,
		environment: env,
	}
	fail := func(err error) (*worktreeSnapshot, error) {
		_ = snapshot.close()
		return nil, err
	}
	if err := fssecure.SecurePrivateDir(tempDir); err != nil {
		return fail(fmt.Errorf("secure snapshot workspace: %w", err))
	}

	gitDir := filepath.Join(tempDir, "repo.git")
	if out, ierr := snapshotGitCommand(ctx, env, "init", "--bare", "--quiet", gitDir).CombinedOutput(); ierr != nil {
		return fail(fmt.Errorf("initialize snapshot object store: %w: %s", ierr, strings.TrimSpace(string(out))))
	}
	commonDir, err := gitOutput(ctx, repoRoot, env, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fail(fmt.Errorf("resolve Git object store: %w", err))
	}
	commonDir = strings.TrimSpace(commonDir)
	alternates := filepath.Join(gitDir, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(commonDir, "objects")+"\n"), 0o600); err != nil {
		return fail(fmt.Errorf("configure snapshot object store: %w", err))
	}

	indexPath := filepath.Join(tempDir, "index")
	objectEnv := append(append([]string{}, env...), []string{
		"GIT_INDEX_FILE=" + indexPath,
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(gitDir, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(commonDir, "objects"),
	}...)

	var tree string
	for attempt := 0; attempt < 3; attempt++ {
		first, terr := captureSnapshotTree(ctx, repoRoot, baseSHA, indexPath, objectEnv, selectedPaths, selection != nil)
		if terr != nil {
			return fail(terr)
		}
		second, terr := captureSnapshotTree(ctx, repoRoot, baseSHA, indexPath, objectEnv, selectedPaths, selection != nil)
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

	if err := rejectGitlinks(ctx, env, gitDir, tree); err != nil {
		return fail(err)
	}
	if err := rejectUnsafeSymlinks(ctx, env, gitDir, tree); err != nil {
		return fail(err)
	}
	if err := rejectSecretShapedFiles(ctx, env, gitDir, tree, baseSHA, allowedSecretFiles); err != nil {
		return fail(err)
	}
	fileCount, sourceBytes, err := measureSnapshotTree(ctx, env, gitDir, tree, limits)
	if err != nil {
		return fail(err)
	}
	sha, err := commitSnapshotTree(ctx, repoRoot, tree, objectEnv)
	if err != nil {
		return fail(err)
	}
	ref := bincache.SeedRef(sha)
	if _, err := gitDirOutput(ctx, env, gitDir, "update-ref", ref, sha); err != nil {
		return fail(fmt.Errorf("name snapshot ref: %w", err))
	}
	bundlePath := filepath.Join(tempDir, "snapshot.bundle")
	if _, err := gitDirOutput(ctx, env, gitDir, "bundle", "create", bundlePath, ref); err != nil {
		return fail(fmt.Errorf("create snapshot bundle: %w", err))
	}
	info, err := os.Stat(bundlePath)
	if err != nil {
		return fail(fmt.Errorf("inspect snapshot bundle: %w", err))
	}
	if info.Size() > maxWorktreeSnapshotBytes {
		return fail(fmt.Errorf("working-tree snapshot bundle is %d bytes; limit is %d bytes", info.Size(), maxWorktreeSnapshotBytes))
	}
	snapshot.SHA = sha
	snapshot.BundlePath = bundlePath
	snapshot.Size = sourceBytes
	snapshot.BundleSize = info.Size()
	snapshot.FileCount = fileCount
	return snapshot, nil
}

func (s *worktreeSnapshot) includeRevisions(ctx context.Context, shas ...string) error {
	gitDir := filepath.Join(s.tempDir, "repo.git")
	refs := []string{bincache.SeedRef(s.SHA)}
	for _, sha := range shas {
		ref := bincache.SeedRef(sha)
		if slices.Contains(refs, ref) {
			continue
		}
		if _, err := gitDirOutput(ctx, s.environment, gitDir, "update-ref", ref, sha); err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	if len(refs) == 1 {
		return nil
	}
	if _, err := gitDirOutput(ctx, s.environment, gitDir, append([]string{"bundle", "create", s.BundlePath}, refs...)...); err != nil {
		return err
	}
	info, err := os.Stat(s.BundlePath)
	if err != nil {
		return err
	}
	if info.Size() > maxWorktreeSnapshotBytes {
		return fmt.Errorf("working-tree snapshot bundle is %d bytes; limit is %d bytes", info.Size(), maxWorktreeSnapshotBytes)
	}
	s.BundleSize = info.Size()
	return nil
}

func resolveSnapshotBaseline(ctx context.Context, env []string, repoRoot string) bincache.WorkspaceBaseline {
	ref := strings.TrimSpace(gitOutputOrEmpty(ctx, env, repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"))
	if ref == "" {
		if gitOutputOrEmpty(ctx, env, repoRoot, "rev-parse", "--verify", "--quiet", "origin/main^{commit}") == "" {
			return bincache.WorkspaceBaseline{}
		}
		ref = "origin/main"
	}
	baseline, err := bincache.WorkspaceBaseline{
		Ref: ref,
		SHA: strings.TrimSpace(gitOutputOrEmpty(ctx, env, repoRoot, "merge-base", ref, "HEAD")),
	}.Resolve()
	if err != nil {
		return bincache.WorkspaceBaseline{}
	}
	return baseline
}

// safety: an unresolved baseline is absent rather than fatal, so a git failure reads as empty.
func gitOutputOrEmpty(ctx context.Context, env []string, repoRoot string, args ...string) string {
	out, err := gitOutput(ctx, repoRoot, env, args...)
	if err != nil {
		return ""
	}
	return out
}

func measureSnapshotTree(ctx context.Context, env []string, gitDir, tree string, limits worktreeSnapshotLimits) (int, int64, error) {
	out, err := gitDirOutput(ctx, env, gitDir, "ls-tree", "-rlz", "--full-tree", tree)
	if err != nil {
		return 0, 0, fmt.Errorf("measure snapshot tree: %w", err)
	}
	count := 0
	var total int64
	for _, raw := range bytes.Split([]byte(out), []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		meta, _, ok := bytes.Cut(raw, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 4 || string(fields[1]) != "blob" {
			return 0, 0, errors.New("measure snapshot tree: malformed blob entry")
		}
		size, err := strconv.ParseInt(string(fields[3]), 10, 64)
		if err != nil || size < 0 || total > limits.bytes-size {
			return 0, 0, fmt.Errorf("working-tree snapshot exceeds the %d-byte uncompressed source limit", limits.bytes)
		}
		count++
		if count > limits.files {
			return 0, 0, fmt.Errorf("working-tree snapshot has more than %d files", limits.files)
		}
		total += size
	}
	return count, total, nil
}

func captureSnapshotTree(ctx context.Context, repoRoot, baseSHA, indexPath string, env []string, selectedPaths string, selected bool) (string, error) {
	if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("reset snapshot index: %w", err)
	}
	seed := baseSHA
	if selected {
		seed = "--empty"
	}
	if _, err := gitOutput(ctx, repoRoot, env, "read-tree", seed); err != nil {
		return "", fmt.Errorf("seed snapshot index: %w", err)
	}
	if selected {
		if selectedPaths != "" {
			cmd := snapshotGitCommand(ctx, env, "--literal-pathspecs", "-C", repoRoot, "-c", "core.safecrlf=false", "add", "-f", "--pathspec-from-file=-", "--pathspec-file-nul")
			cmd.Stdin = strings.NewReader(selectedPaths)
			if _, err := cmd.CombinedOutput(); err != nil {
				return "", fmt.Errorf("capture burst source: %w", snapshotGitError(err))
			}
		}
	} else {
		if _, err := gitOutput(ctx, repoRoot, env, "-c", "core.safecrlf=false", "add", "-A", "--", "."); err != nil {
			return "", fmt.Errorf("capture working tree: %w", snapshotGitError(err))
		}
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
		return fmt.Errorf("inspect snapshot index: %w", snapshotGitError(err))
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
			return fmt.Errorf("inspect snapshot file metadata: %w", pathlessError(statErr))
		}
		if info.Mode().IsRegular() {
			entries = append(entries, snapshotIndexEntry{mode: string(fields[0]), sha: string(fields[1]), path: string(path)})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || string(fields[0]) == "160000" {
			continue
		}
		return errors.New("working-tree snapshot does not support a non-regular file")
	}

	var updates bytes.Buffer
	for start := 0; start < len(entries); {
		end := start
		argumentBytes := 0
		for end < len(entries) && end-start < 64 {
			next := len(entries[end].path) + 1
			if end > start && argumentBytes+next > 8<<10 {
				break
			}
			argumentBytes += next
			end++
		}
		args := []string{"hash-object", "-w", "--no-filters", "--"}
		for _, entry := range entries[start:end] {
			args = append(args, entry.path)
		}
		hashes, hashErr := gitOutput(ctx, repoRoot, env, args...)
		if hashErr != nil {
			return errors.New("hash raw snapshot files failed")
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
		start = end
	}
	if updates.Len() == 0 {
		return nil
	}
	cmd := snapshotGitCommand(ctx, env, "-C", repoRoot, "update-index", "-z", "--index-info")
	cmd.Stdin = &updates
	if _, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("restore raw snapshot files: %w", snapshotGitError(err))
	}
	return nil
}

func commitSnapshotTree(ctx context.Context, repoRoot, tree string, env []string) (string, error) {
	identity := []string{
		"GIT_AUTHOR_NAME=Sparkwing",
		"GIT_AUTHOR_EMAIL=workspace@sparkwing.dev",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Sparkwing",
		"GIT_COMMITTER_EMAIL=workspace@sparkwing.dev",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	cmd := snapshotGitCommand(ctx, env, "-c", "commit.gpgsign=false", "-C", repoRoot, "commit-tree", tree)
	cmd.Env = append(append([]string{}, env...), identity...)
	cmd.Stdin = strings.NewReader("sparkwing working-tree snapshot\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create snapshot commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func rejectGitlinks(ctx context.Context, env []string, gitDir, tree string) error {
	out, err := gitDirOutput(ctx, env, gitDir, "ls-tree", "-r", tree)
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

func rejectUnsafeSymlinks(ctx context.Context, env []string, gitDir, tree string) error {
	out, err := gitDirOutput(ctx, env, gitDir, "ls-tree", "-rz", "--full-tree", tree)
	if err != nil {
		return fmt.Errorf("inspect snapshot symlinks: %w", err)
	}
	targets := map[string]string{}
	for _, record := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[0] != "120000" {
			continue
		}
		target, err := gitDirOutput(ctx, env, gitDir, "cat-file", "blob", fields[2])
		if err != nil {
			return fmt.Errorf("read snapshot symlink: %w", snapshotGitError(err))
		}
		if symlinkTargetAbsolute(target) {
			return errors.New("working-tree snapshot refuses an absolute symlink")
		}
		targets[name] = strings.ReplaceAll(target, "\\", "/")
	}
	for name := range targets {
		if err := resolveSnapshotSymlink(name, targets); err != nil {
			return err
		}
	}
	return nil
}

func symlinkTargetAbsolute(target string) bool {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "\\") {
		return true
	}
	return len(target) >= 2 && ((target[0] >= 'a' && target[0] <= 'z') || (target[0] >= 'A' && target[0] <= 'Z')) && target[1] == ':'
}

func resolveSnapshotSymlink(name string, targets map[string]string) error {
	stack := splitSnapshotPath(pathpkg.Dir(name))
	pending := splitSnapshotPath(targets[name])
	visited := map[string]bool{name: true}
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) == 0 {
				return errors.New("working-tree snapshot refuses an escaping symlink")
			}
			stack = stack[:len(stack)-1]
			continue
		}
		stack = append(stack, part)
		candidate := strings.Join(stack, "/")
		target, ok := targets[candidate]
		if !ok {
			continue
		}
		if visited[candidate] {
			return errors.New("working-tree snapshot refuses a symlink cycle")
		}
		visited[candidate] = true
		stack = stack[:len(stack)-1]
		pending = append(splitSnapshotPath(target), pending...)
	}
	return nil
}

func splitSnapshotPath(value string) []string {
	if value == "." || value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

const secretShapedFileReport = 10

// safety: the manifest travels to another machine, so a credential in it leaks
// to whoever runs the job; the named override keeps the decision per file.
func rejectSecretShapedFiles(ctx context.Context, env []string, gitDir, tree, baseSHA string, allowed []string) error {
	permitted := map[string]bool{}
	for _, path := range allowed {
		if normalized := normalizeSnapshotPath(path); normalized != "" {
			permitted[normalized] = false
		}
	}
	out, err := gitDirOutput(ctx, env, gitDir, "ls-tree", "-rz", "--full-tree", tree)
	if err != nil {
		return fmt.Errorf("inspect snapshot manifest: %w", err)
	}
	keyBlocks, err := snapshotKeyBlockPaths(ctx, env, gitDir, tree)
	if err != nil {
		return err
	}
	var offenders []secretShapedFile
	for _, record := range strings.Split(out, "\x00") {
		meta, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		if _, named := permitted[path]; named {
			permitted[path] = true
			continue
		}
		if envredact.CredentialFileName(path) {
			offenders = append(offenders, secretShapedFile{path: path, reason: "name"})
			continue
		}
		if keyBlocks[path] {
			offenders = append(offenders, secretShapedFile{path: path, reason: "content"})
			continue
		}
		if !envredact.CredentialFileScannable(path) {
			continue
		}
		content, readErr := snapshotBlobPrefix(ctx, env, gitDir, fields[2], envredact.CredentialFilePrefixBytes)
		if readErr != nil {
			return fmt.Errorf("read snapshot file: %w", readErr)
		}
		if envredact.CredentialFileContent(path, content) {
			offenders = append(offenders, secretShapedFile{path: path, reason: "content"})
		}
	}
	var unmatched []string
	for path, matched := range permitted {
		if !matched {
			unmatched = append(unmatched, path)
		}
	}
	if len(unmatched) > 0 {
		sort.Strings(unmatched)
		return fmt.Errorf("--allow-secret-file names %s, which no file in the working-tree snapshot matches; "+
			"the path is repository-relative", strings.Join(unmatched, ", "))
	}
	if len(offenders) == 0 {
		return nil
	}
	return secretShapedFileError(ctx, env, gitDir, baseSHA, offenders)
}

type secretShapedFile struct {
	path   string
	reason string
}

func secretShapedFileError(ctx context.Context, env []string, gitDir, baseSHA string, offenders []secretShapedFile) error {
	tracked := trackedSnapshotPaths(ctx, env, gitDir, baseSHA)
	listed := offenders
	if len(listed) > secretShapedFileReport {
		listed = listed[:secretShapedFileReport]
	}
	described := make([]string, 0, len(listed))
	for _, offender := range listed {
		state := "untracked"
		if tracked[offender.path] {
			state = "tracked"
		}
		described = append(described, fmt.Sprintf("%s (%s, %s)", offender.path, offender.reason, state))
	}
	more := ""
	if len(offenders) > len(listed) {
		more = fmt.Sprintf("\n  and %d more", len(offenders)-len(listed))
	}
	return fmt.Errorf("working-tree snapshot refuses secret-shaped files:\n  %s%s\n"+
		"untrack a tracked file with `git rm --cached PATH`, ignore an untracked one in .gitignore, "+
		"or send one anyway with --allow-secret-file PATH",
		strings.Join(described, "\n  "), more)
}

// safety: an unreadable base commit leaves every offender described as
// untracked, which weakens the remedy line but never admits a file.
func trackedSnapshotPaths(ctx context.Context, env []string, gitDir, baseSHA string) map[string]bool {
	out, err := gitDirOutput(ctx, env, gitDir, "ls-tree", "-rz", "--full-tree", "--name-only", baseSHA)
	if err != nil {
		return nil
	}
	tracked := map[string]bool{}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			tracked[path] = true
		}
	}
	return tracked
}

// safety: a key block travels in any text file, and reading every file to find
// one would cost a process per file; git grep reads the tree once and skips
// binary blobs itself.
func snapshotKeyBlockPaths(ctx context.Context, env []string, gitDir, tree string) (map[string]bool, error) {
	args := []string{"--git-dir", gitDir, "grep", "--full-name", "--files-with-matches", "-I", "-z", "--extended-regexp"}
	for _, pattern := range envredact.CredentialBlockPatterns() {
		args = append(args, "-e", pattern)
	}
	cmd := snapshotGitCommand(ctx, env, append(args, tree)...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("search snapshot for key blocks: %w", snapshotGitError(err))
	}
	found := map[string]bool{}
	for _, record := range strings.Split(string(out), "\x00") {
		path := strings.TrimPrefix(record, tree+":")
		if path == "" || path == record || !envredact.CredentialBlockScannable(path) {
			continue
		}
		found[path] = true
	}
	return found, nil
}

func snapshotBlobPrefix(ctx context.Context, env []string, gitDir, sha string, limit int64) ([]byte, error) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := snapshotGitCommand(readCtx, env, "--git-dir", gitDir, "cat-file", "blob", sha)
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return nil, pipeErr
	}
	if startErr := cmd.Start(); startErr != nil {
		return nil, snapshotGitError(startErr)
	}
	prefix, readErr := io.ReadAll(io.LimitReader(stdout, limit))
	cancel()
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return prefix, nil
}

func normalizeSnapshotPath(path string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	for strings.HasPrefix(cleaned, "./") {
		cleaned = cleaned[2:]
	}
	return strings.TrimPrefix(cleaned, "/")
}

func rejectSelectedWorktreeFilters(ctx context.Context, env []string, repoRoot, selectedPaths string, selected bool) error {
	out := selectedPaths
	if !selected {
		var err error
		out, err = gitOutput(ctx, repoRoot, env, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if err != nil {
			return fmt.Errorf("list snapshot files: %w", err)
		}
	}
	if out == "" {
		return nil
	}
	cmd := snapshotGitCommand(ctx, env, "-C", repoRoot, "check-attr", "-z", "--stdin", "filter", "working-tree-encoding")
	cmd.Stdin = strings.NewReader(out)
	checked, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect Git content filters: %w", snapshotGitError(err))
	}
	parts := bytes.Split(checked, []byte{0})
	for i := 0; i+2 < len(parts); i += 3 {
		value := string(parts[i+2])
		if value == "" || value == "unspecified" || value == "unset" {
			continue
		}
		attribute := string(parts[i+1])
		if attribute == "filter" {
			return fmt.Errorf("working-tree snapshot refuses Git content filter %q; remote materialization would not guarantee the same bytes", value)
		}
		return fmt.Errorf("working-tree snapshot refuses Git %s=%q; remote materialization would not guarantee the same bytes", attribute, value)
	}
	return nil
}

func pathlessError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func snapshotGitError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("git exited with status %d", exitErr.ExitCode())
	}
	return errors.New("git command failed")
}

func gitOutput(ctx context.Context, repoRoot string, env []string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repoRoot}, args...)
	// #nosec G702 -- Callers fix Git subcommands; repository paths are -C operands and filenames follow --.
	cmd := snapshotGitCommand(ctx, env, fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func gitDirOutput(ctx context.Context, env []string, gitDir string, args ...string) (string, error) {
	fullArgs := append([]string{"--git-dir", gitDir}, args...)
	cmd := snapshotGitCommand(ctx, env, fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func snapshotGitCommand(ctx context.Context, env []string, args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		args = append([]string{"-c", "core.longpaths=true"}, args...)
	}
	// #nosec G702 -- fixed Git argv; Git callbacks receive the filtered environment.
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append([]string{}, env...)
	return cmd
}
