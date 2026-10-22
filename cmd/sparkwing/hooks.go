package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/color"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/gitenv"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const sparkwingHookMarker = githooks.Marker

func runHooksInstall(inv clisurface.Invocation) error {
	opts := installOptions{}
	if !inv.Bool("no-prove") {
		opts.prove = runPipelineForProof()
	}
	if inv.Bool("all-repos") {
		if err := installFleet(opts); err != nil {
			return err
		}
		return surveyHooks(false, inv.Output(color.IsInteractiveStdout()))
	}
	repoRoot, sparkwingDir, err := resolveHooksRepo()
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	if _, err := installHooks(runGit, repoRoot, sparkwingDir, opts); err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	return statusHooks(runGit, repoRoot, inv.Output(color.IsInteractiveStdout()))
}

func installFleet(opts installOptions) error {
	roots, err := fleetRepoRoots(runGit)
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "hooks install: no repos registered; run `sparkwing repos add --checkout <dir>` first")
		return nil
	}
	armed, noGate := 0, 0
	var ungated, failed []string
	for _, root := range roots {
		sparkwingDir := filepath.Join(root, ".sparkwing")
		declared, err := declaredHookNames(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\n=== %s\nerror: %v\n", root, err)
			failed = append(failed, filepath.Base(root))
			continue
		}
		if len(declared) == 0 {
			noGate++
			continue
		}
		fmt.Fprintf(os.Stderr, "\n=== %s\n", root)
		gated, err := installHooks(runGit, root, sparkwingDir, opts)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			failed = append(failed, filepath.Base(root))
		case gated:
			armed++
		case len(declaredGates(declared)) == 0:
			noGate++
		default:
			ungated = append(ungated, filepath.Base(root))
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d repo(s) armed, %d with no gate to arm, %d left ungated, %d failed\n",
		armed, noGate, len(ungated), len(failed))
	if len(ungated) > 0 {
		fmt.Fprintf(os.Stderr, "still ungated: %s\n"+
			"  each one printed why above; a gate that cannot run is left unarmed because arming it would fail every commit rather than gate one\n",
			strings.Join(ungated, ", "))
	}
	if len(failed) > 0 {
		return fmt.Errorf("hooks install: %s", strings.Join(failed, ", "))
	}
	return nil
}

func fleetRepoRoots(git repos.Git) ([]string, error) {
	cands, err := repos.CandidatePaths()
	if err != nil {
		return nil, fmt.Errorf("read the repo registry: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, c := range cands {
		root, err := repos.PrimaryRoot(git, c.Path)
		if err != nil || root == "" {
			root = c.Path
		}
		if seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	sort.Strings(out)
	return out, nil
}

type Prover func(repoRoot, pipeline string) error

type installOptions struct {
	prove Prover
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

func declaredHookNames(repoRoot string) ([]string, error) {
	declared, err := declaredHooks(filepath.Join(repoRoot, ".sparkwing"))
	if err != nil {
		return nil, fmt.Errorf("config does not load: %w", err)
	}
	return slices.Sorted(maps.Keys(declared)), nil
}

func installHooks(git githooks.Git, repoRoot, sparkwingDir string, opts installOptions) (bool, error) {
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
	globalHooks := chainableGlobalHooks(git, hooksDir)
	plan, proceed := prepareHookInstall(git, repoRoot, hooksDir,
		prospectiveUnforwardedGlobalHooks(globalHooks, hooksDir), hooksToRun, opts.prove)
	if !proceed {
		declared, err := declaredHookNames(repoRoot)
		if err != nil {
			return false, err
		}
		return githooks.Survey(git, repoRoot, declared).Gated(), errors.New("installation rejected; prior hooks and core.hooksPath remain unchanged")
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, err
	}

	installed, skipped := 0, 0
	for _, hookName := range slices.Sorted(maps.Keys(hooksToRun)) {
		pipes := hooksToRun[hookName]
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, pipes, globalHooks[hookName]))
		if err != nil {
			return false, err
		}
		if !wrote {
			skipped++
			continue
		}
		fmt.Fprintf(os.Stderr, "installed %s -> %s%s\n", hookName, strings.Join(pipes, ", "),
			chainSuffix(globalHooks[hookName]))
		installed++
	}
	for _, hookName := range slices.Sorted(maps.Keys(globalHooks)) {
		if _, ours := hooksToRun[hookName]; ours {
			continue
		}
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, nil, true))
		if err != nil {
			return false, err
		}
		if !wrote {
			skipped++
			continue
		}
		fmt.Fprintf(os.Stderr, "installed %s -> the global hook\n", hookName)
		installed++
	}
	fmt.Fprintf(os.Stderr, "\n%d hook(s) installed, %d skipped\n", installed, skipped)
	gated, err := armHooks(git, repoRoot, hooksDir, hooksToRun, plan)
	if err != nil {
		return false, err
	}
	reportSilencedGlobalHooks(git, repoRoot, hooksDir)
	return gated, nil
}

type hookInstallPlan struct {
	active string
	reads  bool
}

func prepareHookInstall(git githooks.Git, repoRoot, hooksDir string, unforwarded []string, hooksToRun map[string][]string, prove Prover) (hookInstallPlan, bool) {
	active, scope := githooks.ActivePath(git, repoRoot)
	plan := hookInstallPlan{
		active: active, reads: active == "" || githooks.SameDir(active, hooksDir),
	}
	if !plan.reads {
		if scope == "local" {
			fmt.Fprintf(os.Stderr, "\nwarning: git reads hooks from %s, not %s, so nothing was installed\n"+
				"  this repo sets its own core.hooksPath, so the install leaves it alone; clear it with `git -C %s config --unset core.hooksPath`, then re-run `sparkwing pipeline hooks install`\n",
				active, hooksDir, repoRoot)
			return plan, false
		}
		if len(unforwarded) > 0 {
			fmt.Fprintf(os.Stderr, "\nwarning: core.hooksPath left alone: nothing here can hand off to the machine's %s\n"+
				"  claiming it would stop that hook firing in this repo; remove the hook(s) of that name from %s so the install can forward them, then re-run `sparkwing pipeline hooks install`\n"+
				"  until then git keeps reading %s, so nothing was installed\n",
				strings.Join(unforwarded, ", "), hooksDir, active)
			return plan, false
		}
	}
	unproven := proveGates(prove, repoRoot, hooksToRun)
	if len(unproven) == 0 {
		return plan, true
	}
	fmt.Fprintln(os.Stderr, "\nwarning: installation rejected because a required gate proof failed")
	for _, hookName := range slices.Sorted(maps.Keys(unproven)) {
		fmt.Fprintf(os.Stderr, "  %s: %v\n", hookName, unproven[hookName])
	}
	fmt.Fprintln(os.Stderr, "  prior hooks and core.hooksPath remain unchanged")
	fmt.Fprintln(os.Stderr, "  fix the gate(s), then re-run `sparkwing pipeline hooks install`; `--no-prove` arms them without the proof")
	return plan, false
}

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
		if err != nil || info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
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

func prospectiveUnforwardedGlobalHooks(globalHooks map[string]bool, hooksDir string) []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(globalHooks)) {
		body, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err == nil && !strings.Contains(string(body), sparkwingHookMarker) {
			names = append(names, name)
		}
	}
	return names
}

func armHooks(git githooks.Git, repoRoot, hooksDir string, hooksToRun map[string][]string, plan hookInstallPlan) (bool, error) {
	gates := declaredGates(slices.Sorted(maps.Keys(hooksToRun)))
	if len(githooks.Gates(hooksDir)) == 0 {
		if len(gates) > 0 {
			return false, fmt.Errorf("no declared gate runs in %s", hooksDir)
		}
		return false, nil
	}
	if !plan.reads {
		if _, err := git(repoRoot, "config", "core.hooksPath", hooksDir); err != nil {
			return false, fmt.Errorf("claim core.hooksPath for this repo: %w", err)
		}
		fmt.Fprintf(os.Stderr, "\ncore.hooksPath -> %s\n"+
			"  the global core.hooksPath (%s) would otherwise shadow these hooks; its own hooks still fire, chained after this repo's\n",
			hooksDir, plan.active)
	}
	gated := gatesLive(hooksDir, gates)
	if len(gates) > 0 && !gated {
		return false, fmt.Errorf("not all declared gates run in %s", hooksDir)
	}
	return gated, nil
}

func declaredGates(hookNames []string) []string {
	var gates []string
	for _, name := range githooks.BlockingHooks {
		if slices.Contains(hookNames, name) {
			gates = append(gates, name)
		}
	}
	return gates
}

func gatesLive(hooksDir string, declared []string) bool {
	if len(declared) == 0 {
		return false
	}
	live := githooks.Gates(hooksDir)
	for _, name := range declared {
		if !slices.Contains(live, name) {
			return false
		}
	}
	return true
}

func proveGates(prove Prover, repoRoot string, hooksToRun map[string][]string) map[string]error {
	failed := map[string]error{}
	if prove == nil {
		return failed
	}
	for _, hookName := range githooks.BlockingHooks {
		for _, pipeline := range hooksToRun[hookName] {
			fmt.Fprintf(os.Stderr, "\nproving %s (%s) before arming it...\n", pipeline, hookName)
			if err := prove(repoRoot, pipeline); err != nil {
				failed[hookName] = fmt.Errorf("%s did not pass: %w", pipeline, err)
				break
			}
			fmt.Fprintf(os.Stderr, "  %s passed\n", pipeline)
		}
	}
	return failed
}

func runPipelineForProof() Prover {
	return func(repoRoot, pipeline string) error {
		cmd, err := pipelineProofCommand(repoRoot, pipeline)
		if err != nil {
			return err
		}
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		return fmt.Errorf("%w: %s", err, lastLine(out))
	}
}

func pipelineProofCommand(repoRoot, pipeline string) (*exec.Cmd, error) {
	args := []string{"run", pipeline}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate sparkwing executable: %w", err)
	}
	cmd := procenv.ControlCommand(append(os.Environ(), "SPARKWING_LOG_FORMAT=plain"), self, args...)
	cmd.Dir = repoRoot

	return cmd, nil
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return "no output"
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
	releaseHooksPath(git, repoRoot, hooksDir)
	return nil
}

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
	fmt.Fprintln(os.Stderr, "core.hooksPath released; the machine's global hooks apply here again")
}

func runHooksStatus(inv clisurface.Invocation) error {
	mode := inv.Output(color.IsInteractiveStdout())
	if inv.Bool("all-repos") {
		return surveyHooks(inv.Bool("ungated"), mode)
	}
	repoRoot, _, err := resolveHooksRepo()
	if err != nil {
		return fmt.Errorf("hooks status: %w", err)
	}
	return statusHooks(runGit, repoRoot, mode)
}

type managedHookStatus struct {
	Kind         string   `json:"kind"`
	Name         string   `json:"name"`
	Pipelines    []string `json:"pipelines"`
	ChainsGlobal bool     `json:"chains_global"`
	Current      bool     `json:"current"`
}

type hooksStatusSummary struct {
	Kind           string             `json:"kind"`
	Repo           string             `json:"repo"`
	Installed      int                `json:"installed"`
	Gates          githooks.RepoGates `json:"gates"`
	SilencedGlobal []string           `json:"silenced_global"`
}

func statusHooks(git githooks.Git, repoRoot, mode string) error {
	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return err
	}
	expected, _ := expectedHooks(git, repoRoot, hooksDir)
	entries, err := os.ReadDir(hooksDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var hooks []managedHookStatus
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hooksDir, e.Name()))
		if err != nil {
			return fmt.Errorf("read hook %s: %w", e.Name(), err)
		}
		if !strings.Contains(string(data), sparkwingHookMarker) {
			continue
		}
		pipes, chained := describeManagedHook(string(data))
		if pipes == nil {
			pipes = []string{}
		}
		hooks = append(hooks, managedHookStatus{Kind: "hook", Name: e.Name(), Pipelines: pipes, ChainsGlobal: chained, Current: string(data) == expected[e.Name()] && hookExecutable(filepath.Join(hooksDir, e.Name()))})
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
			fmt.Fprintf(os.Stdout, "%s -> %s%s\n", hook.Name, strings.Join(hook.Pipelines, ", "), chainSuffix(hook.ChainsGlobal))
		case hook.ChainsGlobal:
			fmt.Fprintf(os.Stdout, "%s -> the global hook\n", hook.Name)
		default:
			fmt.Fprintf(os.Stdout, "%s (managed)\n", hook.Name)
		}
	}
	if mode == "pretty" && len(hooks) == 0 {
		fmt.Fprintln(os.Stdout, "no sparkwing hooks installed")
		fmt.Fprintln(os.Stdout, "run: sparkwing pipeline hooks install")
	}
	declared, err := declaredHookNames(repoRoot)
	if err != nil {
		return err
	}
	survey := githooks.Survey(git, repoRoot, declared)
	checkExpectedHooks(git, &survey)
	if mode == "plain" {
		if survey.ConfigError != "" {
			fmt.Fprintln(os.Stderr, "warning: "+survey.ConfigError)
		}
		return nil
	}
	if mode == "json" {
		silenced := []string{}
		active, _ := githooks.ActivePath(git, repoRoot)
		if active != "" && githooks.SameDir(active, hooksDir) {
			silenced = append(silenced, unforwardedGlobalHooks(chainableGlobalHooks(git, hooksDir), hooksDir)...)
		}
		return json.NewEncoder(os.Stdout).Encode(hooksStatusSummary{Kind: "summary", Repo: repoRoot, Installed: len(hooks), Gates: survey, SilencedGlobal: silenced})
	}
	if survey.ConfigError != "" || len(survey.NotFiring()) > 0 || len(survey.Borrowed) > 0 {
		fmt.Fprintf(os.Stdout, "\nwarning: %s\n  %s\n", survey.Summary(), survey.Remedy())
	}
	reportSilencedGlobalHooks(git, repoRoot, hooksDir)
	return nil
}

func expectedHooks(git githooks.Git, repoRoot, hooksDir string) (map[string]string, error) {
	declared, err := declaredHooks(filepath.Join(repoRoot, ".sparkwing"))
	if err != nil {
		return nil, err
	}
	global := chainableGlobalHooks(git, hooksDir)
	expected := map[string]string{}
	for name, pipes := range declared {
		expected[name] = renderHookScript(name, pipes, global[name])
	}
	for name := range global {
		if _, ok := expected[name]; !ok {
			expected[name] = renderHookScript(name, nil, true)
		}
	}
	return expected, nil
}

func hookExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0)
}

func checkExpectedHooks(git githooks.Git, survey *githooks.RepoGates) {
	if survey.State != githooks.GateArmed {
		return
	}
	expected, err := expectedHooks(git, survey.Repo, survey.HooksDir)
	if err != nil {
		survey.State, survey.ConfigError = githooks.GateBroken, err.Error()
		return
	}
	var stale []string
	names := append(slices.Collect(maps.Keys(expected)), survey.Installed...)
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		path := filepath.Join(survey.HooksDir, name)
		body, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			survey.State, survey.ConfigError = githooks.GateBroken, fmt.Sprintf("read %s: %v", path, err)
			return
		}
		if err != nil || string(body) != expected[name] || !hookExecutable(path) {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		survey.State, survey.ConfigError = githooks.GateBroken, "hooks missing, not executable, or different from expected content/format: "+strings.Join(stale, ", ")+"; remove obsolete managed hooks, then run sparkwing pipeline hooks install"
	}
}

func reportSilencedGlobalHooks(git githooks.Git, repoRoot, hooksDir string) {
	active, _ := githooks.ActivePath(git, repoRoot)
	if active == "" || !githooks.SameDir(active, hooksDir) {
		return
	}
	silenced := unforwardedGlobalHooks(chainableGlobalHooks(git, hooksDir), hooksDir)
	if len(silenced) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "\nwarning: the machine's global %s does not fire here: git reads %s and nothing there hands off to it\n"+
		"  re-run `sparkwing pipeline hooks install` to restore the forwarder, clearing any hand-written hook of that name first -- install will not overwrite one\n",
		strings.Join(silenced, ", "), hooksDir)
}

func surveyHooks(ungatedOnly bool, format string) error {
	rows, err := surveyFleet(runGit)
	if err != nil {
		return fmt.Errorf("hooks status --all-repos: %w", err)
	}
	if ungatedOnly {
		rows = githooks.Ungated(rows)
	}
	return renderHooksSurvey(os.Stdout, rows, format)
}

func surveyFleet(git githooks.Git) ([]githooks.RepoGates, error) {
	roots, err := fleetRepoRoots(repos.Git(git))
	if err != nil {
		return nil, err
	}
	rows := githooks.SurveyFleet(git, roots, declaredHookNames)
	for i := range rows {
		checkExpectedHooks(git, &rows[i])
	}
	return rows, nil
}

func renderHooksSurvey(w io.Writer, rows []githooks.RepoGates, format string) error {
	switch format {
	case "json":

		return ndjson.Write(w, rows)
	case "plain":

		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Repo, r.State, strings.Join(r.NotFiring(), ","))
		}
		return nil
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no repos registered; run `sparkwing repos add --checkout <dir>` first")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tSTATE\tDECLARED\tFIRING\tNOT FIRING\tBORROWED")
	var ungated, lapsed []githooks.RepoGates
	for _, r := range rows {
		switch {
		case r.State == githooks.GateBroken || !r.RunsBlockingGate():
			ungated = append(ungated, r)
		case len(r.NotFiring()) > 0 || len(r.Borrowed) > 0:
			lapsed = append(lapsed, r)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", filepath.Base(r.Repo), gateWord(r),
			joinOrDash(r.Declared), joinOrDash(r.Fires()), joinOrDash(r.NotFiring()), joinOrDash(r.Borrowed))
	}
	_ = tw.Flush()
	if len(ungated) == 0 && len(lapsed) == 0 {
		fmt.Fprintf(w, "\n%d repo(s), every declared gate fires\n", len(rows))
		return nil
	}
	if len(ungated) > 0 {
		fmt.Fprintf(w, "\n%d of %d repo(s) do not run a gate of their own:\n", len(ungated), len(rows))
		writeSurveyRemedies(w, ungated)
	}
	if len(lapsed) > 0 {
		fmt.Fprintf(w, "\n%d of %d repo(s) refuse a commit but declare a hook that does not fire:\n", len(lapsed), len(rows))
		writeSurveyRemedies(w, lapsed)
	}
	return nil
}

func writeSurveyRemedies(w io.Writer, rows []githooks.RepoGates) {
	for _, r := range rows {
		fmt.Fprintf(w, "  %s\n    %s\n", r.Summary(), r.Remedy())
	}
}

// The STATE column answers "does this repository run a gate", which the raw
// state does not: a repo whose declared hooks all fire is armed even when the
// only one it declares is a notifier that refuses nothing.
func gateWord(r githooks.RepoGates) string {
	if r.State == githooks.GateArmed && !r.Gated() {
		return "no-gate"
	}
	return string(r.State)
}

func joinOrDash(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func describeManagedHook(script string) (pipes []string, chained bool) {
	for line := range strings.SplitSeq(script, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "sparkwing run "); ok {
			pipes = append(pipes, firstShellWord(rest))
		}
		if strings.HasPrefix(line, "exec \"$hook\"") {
			chained = true
		}
	}
	return pipes, chained
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

func renderHookScript(hookName string, pipes []string, chainGlobal bool) string {
	blocking := hookName != "post-commit"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# " + sparkwingHookMarker + " (format 1) -- do not edit; use `sparkwing pipeline hooks (un)install`\n")
	if len(pipes) > 0 {
		b.WriteString("export SPARKWING_LOG_FORMAT=\"${SPARKWING_LOG_FORMAT:-plain}\"\n")
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
	if len(pipes) > 0 {
		b.WriteString("(\n")
		b.WriteString(gitenv.ShellUnbind())
		for _, p := range pipes {
			// safety: pipeline names come from repo config, so quote before they reach sh.
			fmt.Fprintf(&b, "sparkwing run %s%s%s\n", shellSingleQuote(p), stdin, tolerate)
		}
		b.WriteString(")\n")
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

func renderGlobalChain(hookName string) string {
	return "global=\"$(git config --global --type=path core.hooksPath 2>/dev/null)\" || exit 0\n" +
		"[ -n \"$global\" ] || exit 0\n" +
		"hook=\"$global/" + hookName + "\"\n" +
		"[ -x \"$hook\" ] || exit 0\n" +
		"exec \"$hook\" \"$@\"\n"
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
