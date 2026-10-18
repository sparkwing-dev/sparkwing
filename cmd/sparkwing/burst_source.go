package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type burstSourceSelection struct {
	include []string
	exclude []string
}

func burstSourceSnapshot(ctx context.Context, env []string, root string, declared, approved []string) (*worktreeSnapshot, error) {
	gitEnv, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: env, Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		return nil, err
	}
	tracked, err := gitOutput(ctx, root, gitEnv, "ls-files", "--cached", "-z")
	if err != nil {
		return nil, fmt.Errorf("inspect burst source approval: %w", snapshotGitError(err))
	}
	paths := strings.Split(tracked, "\x00")
	var include []string
	for _, name := range declared {
		if slices.Contains(paths, name) {
			continue
		}
		if !slices.Contains(approved, name) {
			return nil, errors.New("run: repository source_files requires approval in machine run.routing.source_files")
		}
		include = append(include, name)
	}
	return captureBurstSnapshot(ctx, env, root, include, nil)
}

func captureBurstSnapshot(ctx context.Context, env []string, start string, includedUntracked, excludedSecretFiles []string) (*worktreeSnapshot, error) {
	return captureSnapshot(ctx, env, start, defaultWorktreeSnapshotLimits, nil, &burstSourceSelection{include: includedUntracked, exclude: excludedSecretFiles})
}

func (s *worktreeSnapshot) branch(ctx context.Context) (string, error) {
	branch, err := gitOutput(ctx, s.RepoRoot, s.environment, "symbolic-ref", "--quiet", "--short", "HEAD")
	if gitExitCode(err, 1) {
		return "", nil
	}
	return strings.TrimSpace(branch), err
}

func checkoutDefaultBranch(ctx context.Context, env []string, root string, masker *secrets.Masker) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("default branch lookup requires an absolute checkout")
	}
	environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: env, Masker: masker}, procenv.Runtime{})
	if err != nil {
		return "", err
	}
	reader := triggerCheckout{directory: root, environment: environment}
	branch, err := reader.read(ctx, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if gitExitCode(err, 1) {
		return "", nil
	}
	return strings.TrimPrefix(strings.TrimSpace(branch), "refs/remotes/origin/"), err
}

func (s burstSourceSelection) paths(ctx context.Context, env []string, repoRoot string) (string, error) {
	for _, names := range [][]string{s.include, s.exclude} {
		if err := projectconfig.ValidateSourceFiles(names); err != nil {
			return "", err
		}
	}
	tracked, err := gitOutput(ctx, repoRoot, env, "ls-files", "--cached", "-z")
	if err != nil {
		return "", fmt.Errorf("list tracked burst source: %w", snapshotGitError(err))
	}
	excluded := func(name string) bool {
		for _, secret := range s.exclude {
			if name == secret || strings.HasPrefix(name, secret+"/") {
				return true
			}
		}
		return false
	}
	files := map[string]bool{}
	for _, name := range strings.Split(tracked, "\x00") {
		if name == "" || excluded(name) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(repoRoot, name)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("inspect tracked burst source: %w", pathlessError(err))
		}
		files[name] = true
	}
	for _, name := range s.include {
		if excluded(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(repoRoot, name))
		if err != nil {
			return "", fmt.Errorf("inspect declared burst source: %w", pathlessError(err))
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return "", errors.New("declared burst source must name a file")
		}
		files[name] = true
	}
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return "", nil
	}
	return strings.Join(paths, "\x00") + "\x00", nil
}
