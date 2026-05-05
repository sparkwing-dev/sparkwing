// Package inputs provides sparkwing.CacheKeyFn helpers for declaring "what
// changed" inputs to a node's cache. Compose them via Compose(...) and
// assign to CacheOptions.CacheKey to skip a node when its inputs match
// a prior successful run.
//
//	import "github.com/sparkwing-dev/sparkwing/sparkwing/inputs"
//
//	sd.Cache(sparkwing.CacheOptions{
//	    Key:      "rangz-web/build-deploy",
//	    OnLimit:  sparkwing.Coalesce,
//	    CacheKey: inputs.Compose(
//	        inputs.RepoFiles(inputs.Ignore("*.md", "docs/**")),
//	        inputs.Env("NEXT_PUBLIC_BACKEND_URL"),
//	        inputs.Const("v1"),
//	    ),
//	})
package inputs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Helpers return sparkwing.CacheKeyFn so they slot directly into
// CacheOptions without wrapping. Compose folds them via sparkwing.Key.
// Hashing errors surface as the empty key (the orchestrator runs the
// node uncached) and log to the node's pipeline logger.

// RepoFilesOption mutates RepoFiles' behavior. Today only Ignore is
// supported; the type exists so future knobs extend without breaking
// call sites.
type RepoFilesOption func(*repoFilesConfig)

type repoFilesConfig struct {
	ignore []string
}

// Ignore excludes paths matching any of the patterns from the
// RepoFiles hash. Pattern semantics mirror a useful subset of
// .gitignore:
//
//   - No slash: basename match anywhere (e.g. "*.md" excludes every
//     markdown file in the tree).
//   - Trailing slash: directory prefix (e.g. "docs/" excludes
//     anything under docs/).
//   - Otherwise: full-path glob with ** for multi-segment wildcard
//     (e.g. "**/*.md" or "docs/**/api.md").
//
// Patterns should describe shape ("any markdown file"), not
// enumerate today's file names.
func Ignore(patterns ...string) RepoFilesOption {
	return func(c *repoFilesConfig) {
		c.ignore = append(c.ignore, patterns...)
	}
}

// RepoFiles returns a sparkwing.CacheKeyFn that hashes the contents
// of every tracked file in the repo. Untracked and gitignored files
// never contribute. Pass Ignore(...) to skip tracked-but-irrelevant
// paths (docs, READMEs).
func RepoFiles(opts ...RepoFilesOption) sparkwing.CacheKeyFn {
	cfg := repoFilesConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	matcher := buildIgnoreMatcher(cfg.ignore)

	return func(ctx context.Context) sparkwing.CacheKey {
		hash, err := hashTrackedFiles(ctx, matcher)
		if err != nil {
			logHashError(ctx, "RepoFiles", err)
			return ""
		}
		return sparkwing.CacheKey("ck:" + hash)
	}
}

// Files returns a sparkwing.CacheKeyFn that hashes the contents of
// tracked files matching the given globs. Patterns share semantics
// with Ignore. Use Files when only a narrow slice of the repo affects
// the step: `Files("src/**", "package.json", "package-lock.json")`.
func Files(globs ...string) sparkwing.CacheKeyFn {
	matcher := buildIncludeMatcher(globs)
	return func(ctx context.Context) sparkwing.CacheKey {
		hash, err := hashTrackedFiles(ctx, matcher)
		if err != nil {
			logHashError(ctx, "Files", err)
			return ""
		}
		return sparkwing.CacheKey("ck:" + hash)
	}
}

// Tree returns a sparkwing.CacheKeyFn that hashes the contents of
// every regular file under root, walking the directory tree without
// consulting git. Use it for inputs outside the calling repo (a
// sibling checkout) or a gitignored build-artifact directory.
//
// root is resolved relative to the calling repo's root so a relative
// path produces the same hash regardless of WorkDir. Symlinks are
// skipped. A missing or non-directory root returns the empty key.
func Tree(root string) sparkwing.CacheKeyFn {
	return func(ctx context.Context) sparkwing.CacheKey {
		abs, err := resolveTreeRoot(ctx, root)
		if err != nil {
			logHashError(ctx, "Tree", err)
			return ""
		}
		hash, err := hashTree(abs)
		if err != nil {
			logHashError(ctx, "Tree", err)
			return ""
		}
		return sparkwing.CacheKey("ck:" + hash)
	}
}

// Env returns a sparkwing.CacheKeyFn that hashes the values of the
// named environment variables. Names are sorted before hashing.
// Missing variables contribute a sentinel ("\x00unset") so a
// removed-but-required var busts the cache rather than silently
// matching.
func Env(names ...string) sparkwing.CacheKeyFn {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return func(_ context.Context) sparkwing.CacheKey {
		var b strings.Builder
		for _, n := range sorted {
			b.WriteString(n)
			b.WriteByte('=')
			if v, ok := os.LookupEnv(n); ok {
				b.WriteString(v)
			} else {
				b.WriteString("\x00unset")
			}
			b.WriteByte('\x1e')
		}
		sum := sha256.Sum256([]byte(b.String()))
		return sparkwing.CacheKey(fmt.Sprintf("ck:%x", sum[:6]))
	}
}

// Const returns a sparkwing.CacheKeyFn that always returns the same
// value. Use it as a cache-busting knob: bump the string ("v1" ->
// "v2") to force every node using this input to re-run.
func Const(s string) sparkwing.CacheKeyFn {
	return func(_ context.Context) sparkwing.CacheKey { return sparkwing.CacheKey(s) }
}

// Compose folds multiple sparkwing.CacheKeyFns into one via
// sparkwing.Key. If any sub-fn returns the empty key, Compose returns
// the empty key (signaling "no cache").
func Compose(fns ...sparkwing.CacheKeyFn) sparkwing.CacheKeyFn {
	return func(ctx context.Context) sparkwing.CacheKey {
		parts := make([]any, 0, len(fns))
		for _, fn := range fns {
			k := fn(ctx)
			if k == "" {
				return ""
			}
			parts = append(parts, string(k))
		}
		return sparkwing.Key(parts...)
	}
}

// ── ignore / include matchers ─────────────────────────────────────────────

type pathMatcher func(path string) bool

func buildIgnoreMatcher(patterns []string) pathMatcher {
	if len(patterns) == 0 {
		return nil
	}
	matchers := compilePatterns(patterns)
	return func(path string) bool {
		for _, m := range matchers {
			if m(path) {
				return false // matched ignore -> drop
			}
		}
		return true // no ignore matched -> keep
	}
}

func buildIncludeMatcher(patterns []string) pathMatcher {
	matchers := compilePatterns(patterns)
	return func(path string) bool {
		for _, m := range matchers {
			if m(path) {
				return true
			}
		}
		return false
	}
}

func compilePatterns(patterns []string) []func(string) bool {
	out := make([]func(string) bool, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, compilePattern(p))
	}
	return out
}

func compilePattern(pattern string) func(string) bool {
	switch {
	case strings.HasSuffix(pattern, "/"):
		prefix := pattern
		return func(path string) bool { return strings.HasPrefix(path, prefix) }

	case !strings.ContainsRune(pattern, '/'):
		// Basename match anywhere
		return func(path string) bool {
			ok, _ := filepath.Match(pattern, filepath.Base(path))
			return ok
		}

	default:
		// Full-path glob with ** support; convert to regex.
		re := globToRegex(pattern)
		return func(path string) bool { return re.MatchString(path) }
	}
}

// globToRegex translates a glob (with **/* support) to a regex
// anchored at both ends.
func globToRegex(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`\A`)
	i := 0
	for i < len(pat) {
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString(`(?:.*/)?`)
			i += 3
		case strings.HasPrefix(pat[i:], "**"):
			b.WriteString(`.*`)
			i += 2
		case pat[i] == '*':
			b.WriteString(`[^/]*`)
			i++
		case pat[i] == '?':
			b.WriteString(`[^/]`)
			i++
		case strings.ContainsRune(`.+()|[]{}^$\`, rune(pat[i])):
			b.WriteByte('\\')
			b.WriteByte(pat[i])
			i++
		default:
			b.WriteByte(pat[i])
			i++
		}
	}
	b.WriteString(`\z`)
	return regexp.MustCompile(b.String())
}

// ── filtered tracked-files hash ───────────────────────────────────────────

// resolveTreeRoot turns Tree's root argument into an absolute path,
// anchoring relative inputs at the repo root.
func resolveTreeRoot(ctx context.Context, root string) (string, error) {
	if filepath.IsAbs(root) {
		return root, nil
	}
	base, err := repoRoot(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Clean(filepath.Join(base, root)), nil
}

// hashTree walks root, collects regular file paths in stable order,
// and hashes their contents. Mirrors hashTrackedFiles' digest shape
// so cache keys from RepoFiles and Tree are interchangeable in
// Compose. Symlinks are skipped.
func hashTree(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Tree: %s is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum)[:12], nil
}

// hashTrackedFiles enumerates `git ls-files` from the repo top-level
// and hashes the working-tree contents of the kept paths in stable
// order. nil keep = keep all.
//
// Reads the working tree (not `git show :path`) so uncommitted edits
// invalidate the cache. Files in the index but missing from disk
// (mid-rename, staged delete) are skipped.
func hashTrackedFiles(ctx context.Context, keep pathMatcher) (string, error) {
	root, err := repoRoot(ctx)
	if err != nil {
		return "", err
	}
	files, err := lsFiles(ctx, root)
	if err != nil {
		return "", err
	}
	if keep != nil {
		filtered := files[:0]
		for _, f := range files {
			if keep(f) {
				filtered = append(filtered, f)
			}
		}
		files = filtered
	}
	sort.Strings(files)

	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			// Missing-on-disk (submodule pointer, staged delete);
			// index-vs-tree mismatch is a normal transient state.
			continue
		}
		h.Write([]byte(f))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum)[:12], nil
}

// repoRoot returns the absolute path of the git repo's working tree
// root. Errors loudly when not in a git repo.
func repoRoot(ctx context.Context) (string, error) {
	out, err := runShell(ctx, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// lsFiles enumerates the tracked files in dir via `git ls-files -z`.
// Output paths are relative to dir; callers pass repoRoot so
// enumeration covers the whole tree regardless of WorkDir().
func lsFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := runShellAt(ctx, dir, "git", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	raw := strings.TrimRight(out, "\x00")
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\x00"), nil
}

// runShell runs in sparkwing.WorkDir() so the cache key reflects the
// repo being built. Refuses to run with an empty WorkDir to avoid
// silently producing a hash for the wrong tree.
func runShell(ctx context.Context, name string, args ...string) (string, error) {
	wd := sparkwing.WorkDir()
	if wd == "" {
		return "", fmt.Errorf("inputs.runShell(%s): %w", name, sparkwing.ErrNoProject)
	}
	return runShellAt(ctx, wd, name, args...)
}

// runShellAt is runShell with an explicit working directory.
func runShellAt(ctx context.Context, dir, name string, args ...string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("inputs.runShellAt(%s): %w", name, sparkwing.ErrNoProject)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// logHashError emits a loud line so a failed CacheKey computation
// is visible. Returning the empty key prevents caching on a partial
// enumeration; the log tells the operator why.
func logHashError(ctx context.Context, fn string, err error) {
	sparkwing.LoggerFromContext(ctx).Log("error",
		fmt.Sprintf("sparkwing.%s: hash failed (proceeding uncached): %v", fn, err))
}
