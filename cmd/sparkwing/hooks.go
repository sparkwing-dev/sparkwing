package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/paths"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const sparkwingHookMarker = githooks.Marker

func runHooksInstall(inv clisurface.Invocation) error {
	repoRoot, sparkwingDir, err := resolveHooksRepo()
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	if _, err := installHooks(runGit, repoRoot, sparkwingDir); err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	return statusHooks(runGit, repoRoot, inv.Output(color.IsInteractiveStdout()))
}

func declaredHooks(sparkwingDir string) (map[string][]string, error) {
	cfg, err := projectconfig.Load(filepath.Join(sparkwingDir, projectconfig.Filename))
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return map[string][]string{}, nil
	}
	out := map[string][]string{}
	for _, p := range cfg.Registry().Pipelines {
		if p.On.PreHook != nil {
			out["pre-commit"] = append(out["pre-commit"], p.Name)
		}
		if p.On.PostHook != nil {
			out["pre-push"] = append(out["pre-push"], p.Name)
		}
		if p.On.PostCommitHook != nil {
			out["post-commit"] = append(out["post-commit"], p.Name)
		}
	}
	return out, nil
}

func installHooks(git githooks.Git, repoRoot, sparkwingDir string) (bool, error) {
	hooksToRun, err := declaredHooks(sparkwingDir)
	if err != nil {
		return false, err
	}
	if len(hooksToRun) == 0 {
		fmt.Fprintln(os.Stderr, "hooks install: no pipelines declare pre_commit, pre_push, or post_commit triggers")
		return false, nil
	}

	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return false, err
	}
	if active, scope := githooks.ActivePath(git, repoRoot); active != "" && !githooks.SameDir(active, hooksDir) {
		return false, fmt.Errorf("%s core.hooksPath points to %s; hooks install leaves it unchanged: %s", scope, active,
			(githooks.Shadow{Repo: repoRoot, Scope: scope, ActiveDir: active, HooksDir: hooksDir}).Remedy())
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, err
	}

	installed, skipped := 0, 0
	for _, hookName := range slices.Sorted(maps.Keys(hooksToRun)) {
		pipes := hooksToRun[hookName]
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, pipes))
		if err != nil {
			return false, err
		}
		if !wrote {
			skipped++
			continue
		}
		fmt.Fprintf(os.Stderr, "installed %s -> %s\n", hookName, strings.Join(pipes, ", "))
		installed++
	}
	fmt.Fprintf(os.Stderr, "\n%d hook(s) installed, %d skipped\n", installed, skipped)
	survey := githooks.Survey(git, repoRoot, expectedHooks)
	if survey.Err != nil {
		return false, survey.Err
	}
	if survey.State == githooks.GateBroken {
		return false, errors.New(survey.Summary())
	}
	return survey.Gated(), nil
}

func writeManagedHook(hooksDir, name, content string) (bool, error) {
	path := filepath.Join(hooksDir, name)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && !strings.Contains(string(existing), sparkwingHookMarker) {
		fmt.Fprintf(os.Stderr, "skipped %s: existing hook is not managed by sparkwing (remove it first)\n", name)
		return false, nil
	}
	if err := replaceHookFile(path, []byte(content), 0o755); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func replaceHookFile(path string, body []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sparkwing-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(mode.Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func runHooksUninstall(inv clisurface.Invocation) error {
	repoRoot, err := resolveCleanupRoot()
	if err != nil {
		return fmt.Errorf("hooks uninstall: %w", err)
	}
	if err := uninstallHooks(runGit, repoRoot); err != nil {
		return fmt.Errorf("hooks uninstall: %w", err)
	}
	return statusHooks(runGit, repoRoot, inv.Output(color.IsInteractiveStdout()))
}

func uninstallHooks(git githooks.Git, repoRoot string) error {
	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no sparkwing hooks installed")
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
			return fmt.Errorf("read hook %s: %w", e.Name(), err)
		}
		if !strings.Contains(string(data), sparkwingHookMarker) {
			continue
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		fmt.Fprintf(os.Stderr, "removed %s\n", e.Name())
		removed++
	}
	if removed == 0 {
		fmt.Fprintln(os.Stderr, "no sparkwing hooks installed")
	} else {
		fmt.Fprintf(os.Stderr, "\n%d hook(s) removed\n", removed)
	}
	return nil
}

func runHooksStatus(inv clisurface.Invocation) error {
	mode := inv.Output(color.IsInteractiveStdout())
	repoRoot, _, err := resolveHooksRepo()
	if err != nil {
		return fmt.Errorf("hooks status: %w", err)
	}
	return statusHooks(runGit, repoRoot, mode)
}

type managedHookStatus struct {
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Pipelines []string `json:"pipelines"`
	Current   bool     `json:"current"`
}

type hooksStatusSummary struct {
	Kind      string             `json:"kind"`
	Repo      string             `json:"repo"`
	Installed int                `json:"installed"`
	Gates     githooks.RepoGates `json:"gates"`
}

func statusHooks(git githooks.Git, repoRoot, mode string) error {
	survey := githooks.Survey(git, repoRoot, expectedHooks)
	var hooks []managedHookStatus
	for _, h := range survey.Hooks {
		if h.Error != "" {
			fmt.Fprintln(os.Stderr, "warning: "+h.Error)
		}
		if !h.Managed {
			continue
		}
		pipes := describeManagedHook(h.Body)
		if pipes == nil {
			pipes = []string{}
		}
		hooks = append(hooks, managedHookStatus{Kind: "hook", Name: h.Name, Pipelines: pipes, Current: h.Current()})
	}
	for _, hook := range hooks {
		if !hook.Current {
			fmt.Fprintf(os.Stderr, "warning: %s differs from the expected hook content or format version; run sparkwing pipeline hooks install\n", hook.Name)
		}
		if mode == "json" {
			if err := json.NewEncoder(os.Stdout).Encode(hook); err != nil {
				return err
			}
			continue
		}
		if mode == "plain" {
			if _, err := fmt.Fprintln(os.Stdout, hook.Name); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(hook.Pipelines) > 0:
			fmt.Fprintf(os.Stdout, "%s -> %s\n", hook.Name, strings.Join(hook.Pipelines, ", "))
		default:
			fmt.Fprintf(os.Stdout, "%s (managed)\n", hook.Name)
		}
	}
	if mode == "pretty" && len(hooks) == 0 {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		fmt.Fprintln(os.Stdout, "run: sparkwing pipeline hooks install")
	}
	if survey.Err != nil {
		return survey.Err
	}
	if mode == "plain" {
		if survey.ConfigError != "" {
			fmt.Fprintln(os.Stderr, "warning: "+survey.ConfigError)
		}
		return nil
	}
	if mode == "json" {
		return json.NewEncoder(os.Stdout).Encode(hooksStatusSummary{Kind: "summary", Repo: repoRoot, Installed: len(hooks), Gates: survey})
	}
	if survey.ConfigError != "" || len(survey.NotFiring()) > 0 || len(survey.Borrowed) > 0 {
		fmt.Fprintf(os.Stdout, "\nwarning: %s\n  %s\n", survey.Summary(), survey.Remedy())
	}
	return nil
}

func expectedHooks(git githooks.Git, repoRoot, hooksDir string) (map[string]githooks.ExpectedHook, error) {
	declared, err := declaredHooks(filepath.Join(repoRoot, ".sparkwing"))
	if err != nil {
		return nil, fmt.Errorf("config does not load: %w", err)
	}
	expected := map[string]githooks.ExpectedHook{}
	for name, pipes := range declared {
		expected[name] = githooks.ExpectedHook{Content: renderHookScript(name, pipes), Declared: true}
	}
	return expected, nil
}

func describeManagedHook(script string) (pipes []string) {
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "sparkwing run "); ok {
			pipes = append(pipes, firstShellWord(rest))
		}
	}
	return pipes
}

// safety: hooks and schedules read only sparkwing.yaml, so a checkout whose
// .sparkwing/ holds no main.go yet still resolves.
func resolveHooksRepo() (repoRoot, sparkwingDir string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	dir, ok, err := nearestDotSparkwing(cwd)
	if err != nil {
		return "", "", err
	}
	if ok {
		return filepath.Dir(dir), dir, nil
	}
	return "", "", fmt.Errorf("no .sparkwing/ directory in %s or above it (pass -C DIR)", cwd)
}

// safety: removal must work on a checkout whose project files are gone, or a
// stale hook keeps refusing commits and a stale schedule keeps firing; so it
// takes the nearest .sparkwing/ with or without markers, then the git root.
func resolveCleanupRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	homes := paths.StateDirs()
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, ".sparkwing")
		if slices.Contains(homes, candidate) {
			break
		}
		info, err := os.Stat(candidate)
		switch {
		case err == nil && info.IsDir():
			return dir, nil
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return "", fmt.Errorf("inspect %s: %w", candidate, err)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	top, err := gitOutput(context.Background(), cwd, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("no .sparkwing/ directory or git checkout in %s or above it (pass -C DIR)", cwd)
	}
	return strings.TrimSpace(top), nil
}

// safety: the runtime home is also named .sparkwing (~/.sparkwing by default),
// so the walk stops there and accepts only a directory carrying a project
// marker; otherwise a checkout with no .sparkwing/ of its own would write its
// project files into the runs store's directory.
func nearestDotSparkwing(start string) (string, bool, error) {
	homes := paths.StateDirs()
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, ".sparkwing")
		if slices.Contains(homes, candidate) {
			return "", false, nil
		}
		ok, err := isProjectDotSparkwing(candidate)
		if err != nil || ok {
			return candidate, ok, err
		}
		if filepath.Dir(dir) == dir {
			return "", false, nil
		}
	}
}

// safety: a .sparkwing/ this process cannot inspect may still be the project,
// so an error other than absence stops the walk rather than letting it land
// on an ancestor's project.
func isProjectDotSparkwing(dir string) (bool, error) {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("inspect %s: %w", dir, err)
	case !info.IsDir():
		return false, nil
	}
	for _, marker := range []string{"sparkwing.yaml", "go.mod"} {
		info, err := os.Stat(filepath.Join(dir, marker))
		switch {
		case err == nil && info.Mode().IsRegular():
			return true, nil
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return false, fmt.Errorf("inspect %s: %w", dir, err)
		}
	}
	return false, nil
}

func renderHookScript(hookName string, pipes []string) string {
	blocking := hookName != "post-commit"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# " + sparkwingHookMarker + " " + githooks.Format + " -- do not edit; use `sparkwing pipeline hooks (un)install`\n")
	if len(pipes) > 0 {
		b.WriteString("export SPARKWING_LOG_FORMAT=\"${SPARKWING_LOG_FORMAT:-plain}\"\n")
	}
	if blocking {
		b.WriteString("set -e\n")
	}
	tolerate := ""
	if !blocking {
		tolerate = " || true"
	}
	if len(pipes) > 0 {
		b.WriteString("(\n")
		b.WriteString(gitenv.ShellUnbind())
		for _, p := range pipes {
			// safety: pipeline names come from repo config, so quote before they reach sh.
			fmt.Fprintf(&b, "sparkwing run %s%s\n", shellSingleQuote(p), tolerate)
		}
		b.WriteString(")\n")
	}
	if !blocking {
		b.WriteString("exit 0\n")
	}
	return b.String()
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstShellWord(s string) string {
	if !strings.HasPrefix(s, "'") {
		word, _, _ := strings.Cut(s, " ")
		return word
	}
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			quoted = !quoted
		case s[i] == '\\' && !quoted && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case s[i] == ' ' && !quoted:
			return b.String()
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
