package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type burstSourceSelection struct {
	include []string
	exclude []string
}

func authorizeBurstSource(ctx context.Context, root string, declared, allowed []string) error {
	tracked, err := gitOutput(ctx, root, nil, "ls-files", "--cached", "-z")
	if err != nil {
		return fmt.Errorf("inspect burst source approval: %w", snapshotGitError(err))
	}
	paths := strings.Split(tracked, "\x00")
	for _, name := range declared {
		if !slices.Contains(paths, name) && !slices.Contains(allowed, name) {
			return errors.New("run: repository source_files requires approval in machine run.routing.source_files")
		}
	}
	return nil
}

func captureBurstSnapshot(ctx context.Context, start string, includedUntracked, excludedSecretFiles []string) (*worktreeSnapshot, error) {
	return captureSnapshot(ctx, start, defaultWorktreeSnapshotLimits, nil, &burstSourceSelection{include: includedUntracked, exclude: excludedSecretFiles})
}

func (s *worktreeSnapshot) branch(ctx context.Context) (string, error) {
	branch, err := gitOutput(ctx, s.RepoRoot, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if gitExitCode(err, 1) {
		return "", nil
	}
	return strings.TrimSpace(branch), err
}

func checkoutDefaultBranch(ctx context.Context, root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("default branch lookup requires an absolute checkout")
	}
	environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
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

func (s burstSourceSelection) paths(ctx context.Context, repoRoot string) (string, error) {
	for _, names := range [][]string{s.include, s.exclude} {
		if err := projectconfig.ValidateSourceFiles(names); err != nil {
			return "", err
		}
	}
	tracked, err := gitOutput(ctx, repoRoot, nil, "ls-files", "--cached", "-z")
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

func (s *worktreeSnapshot) rejectSecrets(ctx context.Context, values map[string]string) (resultErr error) {
	masker := secrets.NewMasker()
	longest := 0
	for _, value := range values {
		lines := slices.DeleteFunc(strings.Split(value, "\n"), func(line string) bool {
			line = strings.TrimSpace(line)
			return strings.HasPrefix(line, "-----BEGIN ") || strings.HasPrefix(line, "-----END ")
		})
		value = strings.Join(lines, "\n")
		if len(value) >= 8 {
			masker.Register(value)
		}
	}
	patterns := slices.DeleteFunc(masker.Values(), func(pattern string) bool { return len(pattern) < 8 })
	for _, pattern := range patterns {
		longest = max(longest, len(pattern))
	}
	if longest == 0 {
		return nil
	}
	gitDir := filepath.Join(s.tempDir, "repo.git")
	changed, err := gitDirOutput(ctx, gitDir, "diff-tree", "-r", "--no-commit-id", "--no-renames", "--diff-filter=ACMT", "--raw", "-z", s.BaseSHA, s.SHA)
	if err != nil {
		return errors.New("inspect source snapshot objects failed")
	}
	var ids strings.Builder
	records := strings.Split(changed, "\x00")
	for i := 0; i+1 < len(records); i += 2 {
		fields := strings.Fields(records[i])
		if len(fields) != 5 {
			return errors.New("inspect changed snapshot blob failed")
		}
		ids.WriteString(fields[3] + "\n")
	}
	objects := ids.String()
	scanCtx, cancel := context.WithCancel(ctx)
	cmd := snapshotGitCommand(scanCtx, "--git-dir", gitDir, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(objects)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return errors.New("open source snapshot scan failed")
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return errors.New("start source snapshot scan failed")
	}
	defer func() {
		if resultErr != nil {
			cancel()
		}
		if err := cmd.Wait(); err != nil && resultErr == nil {
			resultErr = errors.New("source snapshot scan failed")
		}
		cancel()
	}()
	reader := bufio.NewReader(stdout)
	buffer := make([]byte, 64<<10)
	for range strings.Fields(objects) {
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 {
			return errors.New("read source snapshot object failed")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return errors.New("read source snapshot object size failed")
		}
		tail := ""
		for size > 0 {
			n, err := io.ReadFull(reader, buffer[:min(int64(len(buffer)), size)])
			if err != nil {
				return errors.New("read source snapshot contents failed")
			}
			text := tail + string(buffer[:n])
			if slices.ContainsFunc(patterns, func(pattern string) bool { return strings.Contains(text, pattern) }) {
				return errors.New("source bundle contains a declared local secret")
			}
			tail = text[max(0, len(text)-longest+1):]
			size -= int64(n)
		}
		if end, err := reader.ReadByte(); err != nil || end != '\n' {
			return errors.New("read source snapshot object end failed")
		}
	}
	return nil
}
