// Package inputs builds cache keys from files, environment variables, and constants.
package inputs

import (
	"context"
	"crypto/sha256"
	"errors"
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

type RepoFilesOption func(*repoFilesConfig)

type repoFilesConfig struct {
	ignore []string
}

// Ignore excludes matching tracked paths from RepoFiles.
// Patterns without a slash match basenames anywhere in the tree.
// A trailing slash matches a directory prefix.
// Other patterns match full paths; ** spans multiple path segments.
func Ignore(patterns ...string) RepoFilesOption {
	return func(config *repoFilesConfig) {
		config.ignore = append(config.ignore, patterns...)
	}
}

// RepoFiles hashes every tracked file. Ignore excludes matching tracked paths.
func RepoFiles(options ...RepoFilesOption) sparkwing.CacheKeyFn {
	config := repoFilesConfig{}
	for _, option := range options {
		option(&config)
	}
	matcher, patternError := buildIgnoreMatcher(config.ignore)

	return func(ctx context.Context) (sparkwing.CacheKey, error) {
		if patternError != nil {
			return "", patternError
		}
		hash, err := hashTrackedFiles(ctx, matcher)
		if err != nil {
			return "", fmt.Errorf("RepoFiles: %w", err)
		}
		return sparkwing.CacheKey("ck:" + hash), nil
	}
}

// Files hashes tracked files matching the supplied patterns.
// Patterns follow the same rules as Ignore.
func Files(globs ...string) sparkwing.CacheKeyFn {
	matcher, patternError := buildIncludeMatcher(globs)
	return func(ctx context.Context) (sparkwing.CacheKey, error) {
		if patternError != nil {
			return "", patternError
		}
		hash, err := hashTrackedFiles(ctx, matcher)
		if err != nil {
			return "", fmt.Errorf("Files: %w", err)
		}
		return sparkwing.CacheKey("ck:" + hash), nil
	}
}

// Tree hashes every regular file under root and skips symlinks.
// Relative roots resolve against the repository root.
// A missing or non-directory root returns an error.
func Tree(root string) sparkwing.CacheKeyFn {
	return func(ctx context.Context) (sparkwing.CacheKey, error) {
		absolute, err := resolveTreeRoot(ctx, root)
		if err != nil {
			return "", fmt.Errorf("Tree: %w", err)
		}
		hash, err := hashTree(ctx, absolute)
		if err != nil {
			return "", fmt.Errorf("Tree: %w", err)
		}
		return sparkwing.CacheKey("ck:" + hash), nil
	}
}

// Env hashes the named environment variables in sorted name order.
// Unset variables hash differently from empty variables.
func Env(names ...string) sparkwing.CacheKeyFn {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return func(_ context.Context) (sparkwing.CacheKey, error) {
		var builder strings.Builder
		for _, name := range sorted {
			builder.WriteString(name)
			builder.WriteByte('=')
			if value, ok := os.LookupEnv(name); ok {
				builder.WriteString(value)
			} else {
				builder.WriteString("\x00unset")
			}
			builder.WriteByte('\x1e')
		}
		sum := sha256.Sum256([]byte(builder.String()))
		return sparkwing.CacheKey(fmt.Sprintf("ck:%x", sum[:6])), nil
	}
}

// Const returns the supplied key.
func Const(s string) sparkwing.CacheKeyFn {
	return func(_ context.Context) (sparkwing.CacheKey, error) { return sparkwing.CacheKey(s), nil }
}

// Compose combines keys with sparkwing.Key.
// An input error or empty key returns an error.
// NoCache stops evaluation and bypasses memoization.
func Compose(resolvers ...sparkwing.CacheKeyFn) sparkwing.CacheKeyFn {
	return func(ctx context.Context) (sparkwing.CacheKey, error) {
		parts := make([]any, 0, len(resolvers))
		for index, resolve := range resolvers {
			key, err := resolve(ctx)
			if err != nil {
				return "", fmt.Errorf("Compose input %d: %w", index, err)
			}
			if key == sparkwing.NoCache {
				return sparkwing.NoCache, nil
			}
			if key == "" {
				return "", fmt.Errorf("Compose input %d: empty key", index)
			}
			parts = append(parts, string(key))
		}
		return sparkwing.Key(parts...), nil
	}
}

type pathMatcher func(path string) (bool, error)

func buildIgnoreMatcher(patterns []string) (pathMatcher, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	matchers, err := compilePatterns(patterns)
	if err != nil {
		return nil, err
	}
	return func(path string) (bool, error) {
		for _, matcher := range matchers {
			matched, err := matcher(path)
			if err != nil {
				return false, err
			}
			if matched {
				return false, nil
			}
		}
		return true, nil
	}, nil
}

func buildIncludeMatcher(patterns []string) (pathMatcher, error) {
	matchers, err := compilePatterns(patterns)
	if err != nil {
		return nil, err
	}
	return func(path string) (bool, error) {
		for _, matcher := range matchers {
			matched, err := matcher(path)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}, nil
}

func compilePatterns(patterns []string) ([]pathMatcher, error) {
	matchers := make([]pathMatcher, 0, len(patterns))
	for _, pattern := range patterns {
		matcher, err := compilePattern(pattern)
		if err != nil {
			return nil, err
		}
		matchers = append(matchers, matcher)
	}
	return matchers, nil
}

func compilePattern(pattern string) (pathMatcher, error) {
	switch {
	case strings.HasSuffix(pattern, "/"):
		return func(path string) (bool, error) { return strings.HasPrefix(path, pattern), nil }, nil
	case !strings.ContainsRune(pattern, '/'):
		if _, err := filepath.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("cache input pattern %q: %w", pattern, err)
		}
		return func(path string) (bool, error) {
			matched, err := filepath.Match(pattern, filepath.Base(path))
			if err != nil {
				return false, fmt.Errorf("cache input pattern %q: %w", pattern, err)
			}
			return matched, nil
		}, nil
	default:
		expression := globToRegex(pattern)
		return func(path string) (bool, error) { return expression.MatchString(path), nil }, nil
	}
}

func globToRegex(pattern string) *regexp.Regexp {
	var builder strings.Builder
	builder.WriteString(`\A`)
	index := 0
	for index < len(pattern) {
		switch {
		case strings.HasPrefix(pattern[index:], "**/"):
			builder.WriteString(`(?:.*/)?`)
			index += 3
		case strings.HasPrefix(pattern[index:], "**"):
			builder.WriteString(`.*`)
			index += 2
		case pattern[index] == '*':
			builder.WriteString(`[^/]*`)
			index++
		case pattern[index] == '?':
			builder.WriteString(`[^/]`)
			index++
		case strings.ContainsRune(`.+()|[]{}^$\`, rune(pattern[index])):
			builder.WriteByte('\\')
			builder.WriteByte(pattern[index])
			index++
		default:
			builder.WriteByte(pattern[index])
			index++
		}
	}
	builder.WriteString(`\z`)
	return regexp.MustCompile(builder.String())
}

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

func hashTree(ctx context.Context, root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Tree: %s is not a directory", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, relativeError := filepath.Rel(root, path)
		if relativeError != nil {
			return relativeError
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	digest := sha256.New()
	for _, relative := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", relative, err)
		}
		digest.Write([]byte(relative))
		digest.Write([]byte{0})
		digest.Write(contents)
		digest.Write([]byte{0})
	}
	sum := digest.Sum(nil)
	return fmt.Sprintf("%x", sum)[:12], nil
}

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
		for _, file := range files {
			matched, err := keep(file)
			if err != nil {
				return "", err
			}
			if matched {
				filtered = append(filtered, file)
			}
		}
		files = filtered
	}
	sort.Strings(files)

	digest := sha256.New()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contents, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		digest.Write([]byte(file))
		digest.Write([]byte{0})
		digest.Write(contents)
		digest.Write([]byte{0})
	}
	sum := digest.Sum(nil)
	return fmt.Sprintf("%x", sum)[:12], nil
}

func repoRoot(ctx context.Context) (string, error) {
	output, err := runCommand(ctx, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(output, "\n"), nil
}

func lsFiles(ctx context.Context, dir string) ([]string, error) {
	output, err := runCommandAt(ctx, dir, "git", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	raw := strings.TrimRight(output, "\x00")
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\x00"), nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	directory := sparkwing.WorkDir()
	if directory == "" {
		return "", fmt.Errorf("inputs.runCommand(%s): %w", name, sparkwing.ErrNoProject)
	}
	return runCommandAt(ctx, directory, name, args...)
}

func runCommandAt(ctx context.Context, dir, name string, args ...string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("inputs.runCommandAt(%s): %w", name, sparkwing.ErrNoProject)
	}
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		var processError *exec.ExitError
		if errors.As(err, &processError) && len(processError.Stderr) != 0 {
			return "", fmt.Errorf("%s %v in %s: %w: %s", name, args, dir, err, strings.TrimSpace(string(processError.Stderr)))
		}
		return "", fmt.Errorf("%s %v in %s: %w", name, args, dir, err)
	}
	return string(output), nil
}
