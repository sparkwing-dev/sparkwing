package inputs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// RepoFiles hashes every tracked file. Ignore excludes matching tracked paths.
func RepoFiles(options ...RepoFilesOption) sparkwing.MemoKeyFn {
	config := repoFilesConfig{}
	for _, option := range options {
		option(&config)
	}
	matcher, patternError := buildIgnoreMatcher(config.ignore)

	return func(ctx context.Context) (sparkwing.MemoValue, error) {
		if patternError != nil {
			return "", patternError
		}
		hash, err := hashTrackedFiles(ctx, matcher)
		if err != nil {
			return "", fmt.Errorf("RepoFiles: %w", err)
		}
		return sparkwing.MemoValue("memo:" + hash), nil
	}
}

// Files hashes tracked files matching the supplied patterns.
// Patterns follow the same rules as Ignore.
func Files(globs ...string) sparkwing.MemoKeyFn {
	matcher, patternError := buildIncludeMatcher(globs)
	return func(ctx context.Context) (sparkwing.MemoValue, error) {
		if patternError != nil {
			return "", patternError
		}
		hash, err := hashTrackedFiles(ctx, matcher)
		if err != nil {
			return "", fmt.Errorf("Files: %w", err)
		}
		return sparkwing.MemoValue("memo:" + hash), nil
	}
}

// Tree hashes every regular file under root and skips symlinks.
// Relative roots resolve against the repository root.
// A missing or non-directory root returns an error.
func Tree(root string) sparkwing.MemoKeyFn {
	return func(ctx context.Context) (sparkwing.MemoValue, error) {
		absolute, err := resolveTreeRoot(ctx, root)
		if err != nil {
			return "", fmt.Errorf("Tree: %w", err)
		}
		hash, err := hashTree(ctx, absolute)
		if err != nil {
			return "", fmt.Errorf("Tree: %w", err)
		}
		return sparkwing.MemoValue("memo:" + hash), nil
	}
}

// Env hashes the named environment variables in sorted name order.
// Unset variables hash differently from empty variables.
func Env(names ...string) sparkwing.MemoKeyFn {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return func(_ context.Context) (sparkwing.MemoValue, error) {
		digest := sha256.New()
		for _, name := range sorted {
			writeHashPart(digest, []byte(name))
			if value, ok := os.LookupEnv(name); ok {
				writeHashPart(digest, []byte{1})
				writeHashPart(digest, []byte(value))
			} else {
				writeHashPart(digest, []byte{0})
			}
		}
		return sparkwing.MemoValue(fmt.Sprintf("memo:%x", digest.Sum(nil))), nil
	}
}

// Const returns the supplied key.
func Const(s string) sparkwing.MemoKeyFn {
	return func(_ context.Context) (sparkwing.MemoValue, error) {
		if s == "" {
			return "", errors.New("Const: empty memo key")
		}
		return sparkwing.MemoValue(s), nil
	}
}

// Compose combines keys with sparkwing.MemoKey.
// An input error or empty key returns an error.
// NoMemo stops evaluation and bypasses memoization.
func Compose(resolvers ...sparkwing.MemoKeyFn) sparkwing.MemoKeyFn {
	return func(ctx context.Context) (sparkwing.MemoValue, error) {
		parts := make([]any, 0, len(resolvers))
		for index, resolve := range resolvers {
			if resolve == nil {
				return "", fmt.Errorf("Compose input %d: nil resolver", index)
			}
			key, err := resolve(ctx)
			if err != nil {
				return "", fmt.Errorf("Compose input %d: %w", index, err)
			}
			if key == sparkwing.NoMemo {
				return sparkwing.NoMemo, nil
			}
			if key == "" {
				return "", fmt.Errorf("Compose input %d: empty key", index)
			}
			parts = append(parts, string(key))
		}
		return sparkwing.MemoKey(parts...), nil
	}
}
