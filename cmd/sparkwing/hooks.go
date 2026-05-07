// `sparkwing hooks` subcommand. Installs, uninstalls, and reports on
// git hook scripts that fire sparkwing pipelines on commit / push.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/pipelines"
)

// sparkwingHookMarker identifies hook files this command manages.
// Any script containing this string is considered ours for
// uninstall / status purposes.
const sparkwingHookMarker = "Installed by sparkwing"

func runHooks(args []string) error {
	if handleParentHelp(cmdHooks, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdHooks, os.Stderr)
		return errors.New("hooks: subcommand required (install|uninstall|status)")
	}
	switch args[0] {
	case "install":
		return runHooksInstall(args[1:])
	case "uninstall":
		return runHooksUninstall(args[1:])
	case "status":
		return runHooksStatus(args[1:])
	default:
		PrintHelp(cmdHooks, os.Stderr)
		return fmt.Errorf("hooks: unknown subcommand %q", args[0])
	}
}

func runHooksInstall(args []string) error {
	fs := flag.NewFlagSet(cmdHooksInstall.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	if err := parseAndCheck(cmdHooksInstall, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	repoRoot, sparkwingDir, err := resolveHooksRepo(*repo)
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}

	cfg, err := pipelines.Load(filepath.Join(sparkwingDir, "pipelines.yaml"))
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}

	// Collect hook -> pipelines mapping so one hook can fan out to
	// multiple pipelines if needed.
	hooksToRun := map[string][]string{}
	for _, p := range cfg.Pipelines {
		if p.On.PreHook != nil {
			hooksToRun["pre-commit"] = append(hooksToRun["pre-commit"], p.Name)
		}
		if p.On.PostHook != nil {
			hooksToRun["pre-push"] = append(hooksToRun["pre-push"], p.Name)
		}
	}
	if len(hooksToRun) == 0 {
		fmt.Fprintln(os.Stdout, "hooks install: no pipelines declare pre_commit or pre_push triggers")
		return nil
	}

	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}

	installed := 0
	skipped := 0
	for hookName, pipes := range hooksToRun {
		hookPath := filepath.Join(hooksDir, hookName)
		if existing, err := os.ReadFile(hookPath); err == nil {
			if !strings.Contains(string(existing), sparkwingHookMarker) {
				fmt.Fprintf(os.Stdout, "skipped %s: existing hook is not managed by sparkwing (remove it first)\n", hookName)
				skipped++
				continue
			}
		}
		content := renderHookScript(pipes)
		if err := os.WriteFile(hookPath, []byte(content), 0o755); err != nil {
			return fmt.Errorf("hooks install: write %s: %w", hookPath, err)
		}
		fmt.Fprintf(os.Stdout, "installed %s -> %s\n", hookName, strings.Join(pipes, ", "))
		installed++
	}
	fmt.Fprintf(os.Stdout, "\n%d hook(s) installed, %d skipped\n", installed, skipped)
	return nil
}

func runHooksUninstall(args []string) error {
	fs := flag.NewFlagSet(cmdHooksUninstall.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	if err := parseAndCheck(cmdHooksUninstall, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	repoRoot, _, err := resolveHooksRepo(*repo)
	if err != nil {
		return fmt.Errorf("hooks uninstall: %w", err)
	}
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		// No hooks dir means nothing to remove.
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		return nil
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(hooksDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if !strings.Contains(string(data), sparkwingHookMarker) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("hooks uninstall: remove %s: %w", p, err)
		}
		fmt.Fprintf(os.Stdout, "removed %s\n", e.Name())
		removed++
	}
	if removed == 0 {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		return nil
	}
	fmt.Fprintf(os.Stdout, "\n%d hook(s) removed\n", removed)
	return nil
}

func runHooksStatus(args []string) error {
	fs := flag.NewFlagSet(cmdHooksStatus.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	if err := parseAndCheck(cmdHooksStatus, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	repoRoot, _, err := resolveHooksRepo(*repo)
	if err != nil {
		return fmt.Errorf("hooks status: %w", err)
	}
	hooksDir := filepath.Join(repoRoot, ".git", "hooks")
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		return nil
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(hooksDir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if !strings.Contains(string(data), sparkwingHookMarker) {
			continue
		}
		// Extract pipeline names from `wing <name>` lines so status
		// is informative not just "yes it's there."
		var pipes []string
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "wing ") {
				pipes = append(pipes, strings.TrimPrefix(line, "wing "))
			}
		}
		if len(pipes) > 0 {
			fmt.Fprintf(os.Stdout, "%s -> %s\n", e.Name(), strings.Join(pipes, ", "))
		} else {
			fmt.Fprintf(os.Stdout, "%s (managed)\n", e.Name())
		}
		found++
	}
	if found == 0 {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		fmt.Fprintln(os.Stdout, "run: sparkwing hooks install")
		return nil
	}
	return nil
}

// resolveHooksRepo returns the repo root + .sparkwing dir for the
// given --repo flag. Empty --repo triggers the usual findSparkwingDir
// walk from cwd.
func resolveHooksRepo(repo string) (repoRoot, sparkwingDir string, err error) {
	if repo == "" {
		dir, err := findSparkwingDir()
		if err != nil {
			return "", "", err
		}
		return filepath.Dir(dir), dir, nil
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", "", err
	}
	candidate := filepath.Join(abs, ".sparkwing")
	if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("no .sparkwing/ directory under %s", abs)
	}
	return abs, candidate, nil
}

// renderHookScript builds the hook file contents. Short POSIX sh so
// it runs anywhere git does; exits non-zero on first pipeline failure
// so git aborts the commit / push as operators expect.
func renderHookScript(pipes []string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# " + sparkwingHookMarker + " -- do not edit; use `sparkwing hooks (un)install`\n")
	b.WriteString("set -e\n")
	for _, p := range pipes {
		fmt.Fprintf(&b, "wing %s\n", p)
	}
	return b.String()
}
