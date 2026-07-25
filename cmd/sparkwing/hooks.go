// `sparkwing pipeline hooks` subcommand. Installs, uninstalls, and reports on
// git hook scripts that fire sparkwing pipelines on commit / push.
package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// sparkwingHookMarker identifies hook files this command manages.
// Any script containing this string is considered ours for
// uninstall / status purposes.
const sparkwingHookMarker = githooks.Marker

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
	if err := installHooks(runGit, repoRoot, sparkwingDir); err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	return nil
}

// installHooks writes a managed hook for every trigger repoRoot's pipelines
// declare, then makes sure git will actually read them.
//
// A core.hooksPath in the machine's global git config replaces .git/hooks
// for every repository, so an install that only writes files leaves the gate
// dead. The install therefore claims core.hooksPath for this repository and
// chains the global hooks from it: a hook name both layers define runs the
// pipeline first and hands off to the global hook after, and a name only the
// global layer defines gets a forwarder. Nothing the machine configured is
// dropped, and nothing sparkwing installed is shadowed.
//
// The claim is only safe once every global hook name has a forwarder behind
// it, so a name whose forwarder could not be written -- a hand-written hook
// sits there -- holds the claim back rather than silencing the machine's
// hook. A repository already carrying the claim reaches no such decision, so
// the install reports any global hook nothing hands off to on its way out.
func installHooks(git githooks.Git, repoRoot, sparkwingDir string) error {
	cfg, err := projectconfig.Load(filepath.Join(sparkwingDir, projectconfig.Filename))
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &projectconfig.Config{}
	}

	hooksToRun := map[string][]string{}
	for _, p := range cfg.Pipelines {
		if p.On.PreHook != nil {
			hooksToRun["pre-commit"] = append(hooksToRun["pre-commit"], p.Name)
		}
		if p.On.PostHook != nil {
			hooksToRun["pre-push"] = append(hooksToRun["pre-push"], p.Name)
		}
		if p.On.PostCommitHook != nil {
			hooksToRun["post-commit"] = append(hooksToRun["post-commit"], p.Name)
		}
	}
	if len(hooksToRun) == 0 {
		fmt.Fprintln(os.Stdout, "hooks install: no pipelines declare pre_commit, pre_push, or post_commit triggers")
		return nil
	}

	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}

	globalHooks := chainableGlobalHooks(git, hooksDir)

	installed, skipped := 0, 0
	for _, hookName := range slices.Sorted(maps.Keys(hooksToRun)) {
		pipes := hooksToRun[hookName]
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, pipes, globalHooks[hookName]))
		if err != nil {
			return err
		}
		if !wrote {
			skipped++
			continue
		}
		fmt.Fprintf(os.Stdout, "installed %s -> %s%s\n", hookName, strings.Join(pipes, ", "),
			chainSuffix(globalHooks[hookName]))
		installed++
	}
	for _, hookName := range slices.Sorted(maps.Keys(globalHooks)) {
		if _, ours := hooksToRun[hookName]; ours {
			continue
		}
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, nil, true))
		if err != nil {
			return err
		}
		if !wrote {
			skipped++
			continue
		}
		fmt.Fprintf(os.Stdout, "installed %s -> the global hook\n", hookName)
		installed++
	}
	fmt.Fprintf(os.Stdout, "\n%d hook(s) installed, %d skipped\n", installed, skipped)
	if err := claimHooksPath(git, repoRoot, hooksDir, unforwardedGlobalHooks(globalHooks, hooksDir)); err != nil {
		return err
	}
	reportSilencedGlobalHooks(git, repoRoot, hooksDir)
	return nil
}

// unforwardedGlobalHooks returns the global hook names hooksDir does not hand
// off to, sorted. Reading the directory back rather than trusting what the
// install just wrote keeps the answer true of hooks a previous run left
// behind, and of names an unmanaged file displaced.
func unforwardedGlobalHooks(globalHooks map[string]bool, hooksDir string) []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(globalHooks)) {
		data, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil || !strings.Contains(string(data), sparkwingHookMarker) {
			names = append(names, name)
			continue
		}
		if _, chained := describeManagedHook(string(data)); !chained {
			names = append(names, name)
		}
	}
	return names
}

// gitHookNames are the hook names git runs, per githooks(5). A hooks
// directory is a plain directory an operator may also keep helper scripts
// and notes in, and only a name git would ever execute is worth forwarding
// -- or worth holding the hooks-path claim back.
var gitHookNames = map[string]bool{
	"applypatch-msg": true, "pre-applypatch": true, "post-applypatch": true,
	"pre-commit": true, "pre-merge-commit": true, "prepare-commit-msg": true,
	"commit-msg": true, "post-commit": true, "pre-rebase": true,
	"post-checkout": true, "post-merge": true, "pre-push": true,
	"pre-receive": true, "update": true, "proc-receive": true,
	"post-receive": true, "post-update": true, "reference-transaction": true,
	"push-to-checkout": true, "pre-auto-gc": true, "post-rewrite": true,
	"sendemail-validate": true, "fsmonitor-watchman": true,
	"p4-changelist": true, "p4-prepare-changelist": true,
	"p4-post-changelist": true, "p4-pre-submit": true,
	"post-index-change": true,
}

// chainableGlobalHooks returns the hook names the machine's global
// core.hooksPath directory offers, which the install has to keep alive once
// it claims the hooks path for this repository. A global directory that is
// already this repository's own hook directory chains nothing -- forwarding
// it to itself would recurse.
func chainableGlobalHooks(git githooks.Git, hooksDir string) map[string]bool {
	dir := githooks.GlobalPath(git)
	names := map[string]bool{}
	if dir == "" || githooks.SameDir(dir, hooksDir) {
		return names
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return names
	}
	for _, e := range entries {
		if !gitHookNames[e.Name()] {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		names[e.Name()] = true
	}
	return names
}

func chainSuffix(chained bool) string {
	if chained {
		return ", then the global hook"
	}
	return ""
}

// writeManagedHook writes a sparkwing-managed hook, refusing to overwrite a
// hook the operator wrote by hand. It reports whether the file was written.
func writeManagedHook(hooksDir, name, content string) (bool, error) {
	path := filepath.Join(hooksDir, name)
	if existing, err := os.ReadFile(path); err == nil && !strings.Contains(string(existing), sparkwingHookMarker) {
		fmt.Fprintf(os.Stdout, "skipped %s: existing hook is not managed by sparkwing (remove it first)\n", name)
		return false, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// claimHooksPath points git at hooksDir for this repository when a
// core.hooksPath override would otherwise shadow the hooks just installed.
// A machine-wide override is claimed away, since the install has already
// chained its hooks; an override the repository itself carries was set
// deliberately, so it is reported instead of overwritten.
//
// unforwarded names the machine's hooks nothing in hooksDir hands off to.
// Claiming the path while any remain would trade one silent gate for another,
// so the claim is refused and the hook to clear is named.
func claimHooksPath(git githooks.Git, repoRoot, hooksDir string, unforwarded []string) error {
	shadow, err := githooks.Detect(git, repoRoot)
	if err != nil || shadow == nil {
		return err
	}
	if shadow.Scope == "local" {
		fmt.Fprintf(os.Stdout, "\nwarning: %s\n  %s\n", shadow.Summary(), shadow.Remedy())
		return nil
	}
	if len(unforwarded) > 0 {
		fmt.Fprintf(os.Stdout, "\nwarning: core.hooksPath left alone: nothing here hands off to the machine's %s\n"+
			"  claiming it would stop that hook firing in this repo; remove the hook(s) of that name from %s so the install can forward them, then re-run `sparkwing pipeline hooks install`\n"+
			"  until then git keeps reading %s, so the hooks just installed do not fire\n",
			strings.Join(unforwarded, ", "), hooksDir, shadow.ActiveDir)
		return nil
	}
	if _, err := git(repoRoot, "config", "core.hooksPath", hooksDir); err != nil {
		return fmt.Errorf("claim core.hooksPath for this repo: %w", err)
	}
	fmt.Fprintf(os.Stdout, "\ncore.hooksPath -> %s\n"+
		"  the global core.hooksPath (%s) would otherwise shadow these hooks; its own hooks still fire, chained after this repo's\n",
		hooksDir, shadow.ActiveDir)
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
	if err := uninstallHooks(runGit, repoRoot); err != nil {
		return fmt.Errorf("hooks uninstall: %w", err)
	}
	return nil
}

// uninstallHooks removes every hook sparkwing manages in repoRoot, including
// the forwarders that kept the machine's global hooks reachable, and then
// releases the hooks-path claim so those global hooks apply again.
//
// The claim is released whether or not this run removed anything: hooks that
// vanished by other means leave the same claim pointing at a directory with
// no forwarders in it, which is the state the release exists to prevent.
func uninstallHooks(git githooks.Git, repoRoot string) error {
	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		releaseHooksPath(git, repoRoot, hooksDir)
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
			return fmt.Errorf("remove %s: %w", p, err)
		}
		fmt.Fprintf(os.Stdout, "removed %s\n", e.Name())
		removed++
	}
	if removed == 0 {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
	} else {
		fmt.Fprintf(os.Stdout, "\n%d hook(s) removed\n", removed)
	}
	releaseHooksPath(git, repoRoot, hooksDir)
	return nil
}

// releaseHooksPath undoes the hooks-path claim an install made. The claim
// only exists to outrank a global core.hooksPath, and the forwarders that
// kept the machine's hooks reachable are gone with the rest, so leaving it
// in place would strand those hooks. A hooks path pointing anywhere else is
// the operator's and is left alone.
func releaseHooksPath(git githooks.Git, repoRoot, hooksDir string) {
	if githooks.GlobalPath(git) == "" {
		return
	}
	local := githooks.LocalPath(git, repoRoot)
	if local == "" || !githooks.SameDir(local, hooksDir) {
		return
	}
	if _, err := git(repoRoot, "config", "--unset", "core.hooksPath"); err != nil {
		return
	}
	fmt.Fprintln(os.Stdout, "core.hooksPath released; the machine's global hooks apply here again")
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
	if err := statusHooks(runGit, repoRoot); err != nil {
		return fmt.Errorf("hooks status: %w", err)
	}
	return nil
}

// statusHooks reports the managed hooks installed in repoRoot, what each
// one does, and whether git is reading them at all.
func statusHooks(git githooks.Git, repoRoot string) error {
	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return err
	}
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
		pipes, chained := describeManagedHook(string(data))
		switch {
		case len(pipes) > 0:
			fmt.Fprintf(os.Stdout, "%s -> %s%s\n", e.Name(), strings.Join(pipes, ", "), chainSuffix(chained))
		case chained:
			fmt.Fprintf(os.Stdout, "%s -> the global hook\n", e.Name())
		default:
			fmt.Fprintf(os.Stdout, "%s (managed)\n", e.Name())
		}
		found++
	}
	if found == 0 {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		fmt.Fprintln(os.Stdout, "run: sparkwing pipeline hooks install")
		return nil
	}
	shadow, err := githooks.Detect(git, repoRoot)
	if err == nil && shadow != nil {
		fmt.Fprintf(os.Stdout, "\nwarning: %s\n  %s\n", shadow.Summary(), shadow.Remedy())
	}
	reportSilencedGlobalHooks(git, repoRoot, hooksDir)
	return nil
}

// reportSilencedGlobalHooks warns about the machine's global hooks that stop
// firing because git reads this repository's hook directory and nothing there
// hands off to them. It only applies while the repository's hooks are the
// ones git reads: anywhere else the global hooks are still in charge.
func reportSilencedGlobalHooks(git githooks.Git, repoRoot, hooksDir string) {
	active, _ := githooks.ActivePath(git, repoRoot)
	if active == "" || !githooks.SameDir(active, hooksDir) {
		return
	}
	silenced := unforwardedGlobalHooks(chainableGlobalHooks(git, hooksDir), hooksDir)
	if len(silenced) == 0 {
		return
	}
	fmt.Fprintf(os.Stdout, "\nwarning: the machine's global %s does not fire here: git reads %s and nothing there hands off to it\n"+
		"  re-run `sparkwing pipeline hooks install` to restore the forwarder, clearing any hand-written hook of that name first -- install will not overwrite one\n",
		strings.Join(silenced, ", "), hooksDir)
}

// describeManagedHook reads back what a managed hook script does: the
// pipelines it runs, and whether it hands off to the machine's global hook
// of the same name afterwards.
func describeManagedHook(script string) (pipes []string, chained bool) {
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "sparkwing run "); ok {
			name, _, _ := strings.Cut(rest, " ")
			pipes = append(pipes, name)
		}
		if strings.HasPrefix(line, "exec \"$hook\"") {
			chained = true
		}
	}
	return pipes, chained
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

// renderHookScript builds the hook file contents. Short POSIX sh so it
// runs anywhere git does.
//
// Blocking hooks (pre-commit, pre-push) exit non-zero on the first
// pipeline failure so git aborts the commit / push as operators expect.
// The post-commit hook is non-blocking: the commit has already landed,
// so it runs every pipeline, tolerates failures, and always exits zero
// rather than leaving git reporting a failed post-commit step.
//
// chainGlobal appends a hand-off to the same-named hook in the machine's
// global core.hooksPath, which this repository's own hooks path override
// would otherwise shadow. The pipelines run first and the hand-off replaces
// the shell, so the global hook decides the hook's exit status; a failed
// blocking pipeline aborts before reaching it, which is what the operator
// asked for. Pipelines read from /dev/null in that case so the hook's own
// stdin -- the ref list git feeds pre-push -- reaches the global hook
// untouched. Passing no pipelines renders a pure forwarder, for hook names
// only the global layer defines.
//
// The global path is resolved when the hook runs rather than baked in, so
// changing the machine's hooks directory does not require reinstalling.
func renderHookScript(hookName string, pipes []string, chainGlobal bool) string {
	blocking := hookName != "post-commit"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# " + sparkwingHookMarker + " -- do not edit; use `sparkwing pipeline hooks (un)install`\n")
	if len(pipes) > 0 {
		b.WriteString("export SPARKWING_LOG_FORMAT=\"${SPARKWING_LOG_FORMAT:-quiet}\"\n")
	}
	if blocking {
		b.WriteString("set -e\n")
	}
	stdin, tolerate := "", ""
	if chainGlobal {
		stdin = " </dev/null"
	}
	if !blocking {
		tolerate = " || true"
	}
	for _, p := range pipes {
		fmt.Fprintf(&b, "sparkwing run %s%s%s\n", p, stdin, tolerate)
	}
	if chainGlobal {
		b.WriteString(renderGlobalChain(hookName))
		return b.String()
	}
	if !blocking {
		b.WriteString("exit 0\n")
	}
	return b.String()
}

// renderGlobalChain is the tail that hands a hook off to the same-named hook
// in the machine's global core.hooksPath. A machine that sets no global
// hooks path, or offers no such hook, ends the script cleanly.
func renderGlobalChain(hookName string) string {
	return "global=\"$(git config --global --type=path core.hooksPath 2>/dev/null)\" || exit 0\n" +
		"[ -n \"$global\" ] || exit 0\n" +
		"hook=\"$global/" + hookName + "\"\n" +
		"[ -x \"$hook\" ] || exit 0\n" +
		"exec \"$hook\" \"$@\"\n"
}
