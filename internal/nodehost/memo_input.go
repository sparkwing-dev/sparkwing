package nodehost

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func memoValueKey(value any) (string, error) {
	key, err := runspec.CanonicalHash([]any{"memo-v2", value})
	return string(key), err
}

func memoInputValue(ctx context.Context, input nodeproto.MemoInput, workspace string, environment procenv.Env) (any, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	var value any
	var err error
	switch input.Kind {
	case "parts":
		value = append([]any{}, input.Parts...)
	case "compose":
		values := make([]any, 0, len(input.Inputs))
		for _, child := range input.Inputs {
			v, err := memoInputValue(ctx, child, workspace, environment)
			if err != nil {
				return nil, err
			}
			values = append(values, v)
		}
		value = values
	case "env":
		values := map[string]any{}
		for _, name := range input.Names {
			values[name] = nil
		}
		for assignment := range environment.Values() {
			name, v, ok := strings.Cut(assignment, "=")
			if _, wanted := values[name]; ok && wanted {
				values[name] = v
			}
		}
		value = values
	case "files", "repo_files":
		var keep pathMatcher
		if input.Kind == "files" {
			keep, err = buildIncludeMatcher(input.Patterns)
		} else {
			keep, err = buildIgnoreMatcher(input.Patterns)
		}
		if err == nil {
			var root string
			root, err = memoCheckoutRoot(ctx, workspace, environment)
			if err == nil {
				value, err = hashTrackedFiles(ctx, root, keep, environment)
			}
		}
	case "tree":
		root := input.Root
		if !filepath.IsAbs(root) {
			var base string
			base, err = memoCheckoutRoot(ctx, workspace, environment)
			if err == nil {
				root = filepath.Join(base, root)
			}
		}
		if err == nil {
			value, err = hashTree(ctx, root)
		}
	default:
		return nil, errors.New("unknown memo input kind")
	}
	if err != nil {
		return nil, err
	}
	return []any{input.Kind, value}, context.Cause(ctx)
}

func memoCheckoutRoot(ctx context.Context, workspace string, environment procenv.Env) (string, error) {
	root, err := runCommandAt(ctx, environment, workspace, "git", "rev-parse", "--show-toplevel")
	return strings.TrimRight(root, "\n"), err
}

func memoNeedsEnvironment(input nodeproto.MemoInput) bool {
	if input.Kind == "env" || input.Kind == "files" || input.Kind == "repo_files" || input.Kind == "tree" && !filepath.IsAbs(input.Root) {
		return true
	}
	for _, child := range input.Inputs {
		if memoNeedsEnvironment(child) {
			return true
		}
	}
	return false
}

func memoWorkspace(input NodeInput) string {
	if input.Entry == nil {
		return ""
	}
	return sdkWorkingDirectory(input.Entry)
}
