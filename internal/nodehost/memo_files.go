package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"os/exec"
	pathmatch "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

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
		if _, err := pathmatch.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("memo input pattern %q: %w", pattern, err)
		}
		return func(path string) (bool, error) {
			matched, err := pathmatch.Match(pattern, pathmatch.Base(path))
			if err != nil {
				return false, fmt.Errorf("memo input pattern %q: %w", pattern, err)
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

func hashTree(ctx context.Context, root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if resolved != filepath.Clean(root) {
		return "", fmt.Errorf("Tree: %s traverses a symlink", root)
	}
	info, err := os.Lstat(root)
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
		paths = append(paths, filepath.ToSlash(relative))
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
		writeHashPart(digest, []byte(relative))
		writeHashPart(digest, contents)
	}
	sum := digest.Sum(nil)
	return fmt.Sprintf("%x", sum), nil
}

func hashTrackedFiles(ctx context.Context, root string, keep pathMatcher, environment procenv.Env) (string, error) {
	files, err := lsFiles(ctx, environment, root)
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
		path := filepath.Join(root, file)
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		var contents []byte
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return "", fmt.Errorf("read %s: %w", file, err)
			}
			contents = []byte(target)
		} else {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("read %s: %w", file, &os.PathError{Op: "read", Path: path, Err: errors.New("memo input is not a regular file")})
			}
			contents, err = os.ReadFile(path)
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		writeHashPart(digest, []byte(file))
		if info.Mode()&os.ModeSymlink != 0 {
			writeHashPart(digest, []byte{1})
		} else {
			writeHashPart(digest, []byte{0})
		}
		writeHashPart(digest, contents)
	}
	sum := digest.Sum(nil)
	return fmt.Sprintf("%x", sum), nil
}

func writeHashPart(digest hash.Hash, part []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(part)))
	digest.Write(length[:])
	digest.Write(part)
}

func lsFiles(ctx context.Context, environment procenv.Env, dir string) ([]string, error) {
	output, err := runCommandAt(ctx, environment, dir, "git", "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	raw := strings.TrimRight(output, "\x00")
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\x00"), nil
}

func runCommandAt(ctx context.Context, environment procenv.Env, dir, name string, args ...string) (string, error) {
	if dir == "" {
		return "", errors.New("memo requires a checkout")
	}
	command := procenv.CommandContext(ctx, environment, name, args...)
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
