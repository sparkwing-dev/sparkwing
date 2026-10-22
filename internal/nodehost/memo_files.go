package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
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

// Each directory handle is checked before use because entries can change during traversal.
func openMemoDirectory(root *os.Root, name string) (*os.Root, error) {
	if !filepath.IsLocal(name) {
		return nil, fmt.Errorf("memo directory must be root-relative: %s", name)
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(filepath.Clean(name), string(filepath.Separator)) {
		info, err := current.Lstat(part)
		if err == nil && !info.IsDir() {
			err = fmt.Errorf("memo directory %s is not a directory or is a symlink", name)
		}
		if err != nil {
			current.Close()
			return nil, err
		}
		next, err := current.OpenRoot(part)
		current.Close()
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err == nil && !os.SameFile(info, opened) {
			err = fmt.Errorf("memo directory changed during traversal: %s", name)
		}
		if err != nil {
			next.Close()
			return nil, err
		}
		current = next
	}
	return current, nil
}

func hashTree(ctx context.Context, checkout, name string) (string, error) {
	base, err := os.OpenRoot(checkout)
	if err != nil {
		return "", err
	}
	defer base.Close()
	root, err := openMemoDirectory(base, name)
	if err != nil {
		return "", err
	}
	defer root.Close()
	var paths []string
	err = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() && entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hashMemoFiles(ctx, root, paths, false)
}

func hashTrackedFiles(ctx context.Context, root string, keep pathMatcher, environment procenv.Env) (string, error) {
	scoped, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer scoped.Close()
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
	return hashMemoFiles(ctx, scoped, files, true)
}

func hashMemoFiles(ctx context.Context, root *os.Root, files []string, tracked bool) (string, error) {
	sort.Strings(files)
	digest := sha256.New()
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contents, symlink, err := readMemoFile(root, filepath.FromSlash(file), tracked)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", file, err)
		}
		writeHashPart(digest, []byte(file))
		if tracked {
			kind := byte(0)
			if symlink {
				kind = 1
			}
			writeHashPart(digest, []byte{kind})
		}
		writeHashPart(digest, contents)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func readMemoFile(root *os.Root, name string, tracked bool) ([]byte, bool, error) {
	if !filepath.IsLocal(name) {
		return nil, false, fmt.Errorf("memo file must be root-relative: %s", name)
	}
	parent, err := openMemoDirectory(root, filepath.Dir(name))
	if err != nil {
		return nil, false, err
	}
	defer parent.Close()
	name = filepath.Base(name)
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, false, err
	}
	if tracked && info.Mode()&os.ModeSymlink != 0 {
		target, err := parent.Readlink(name)
		return []byte(target), true, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, &os.PathError{Op: "read", Path: filepath.Join(parent.Name(), name), Err: errors.New("memo input is not a regular file")}
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	// Compare the opened file before reading, including a replacement by an in-root symlink.
	opened, err := file.Stat()
	if err == nil && !os.SameFile(info, opened) {
		err = errors.New("memo input changed before reading")
	}
	if err != nil {
		return nil, false, err
	}
	contents, err := io.ReadAll(file)
	return contents, false, err
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
