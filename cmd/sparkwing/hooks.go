package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
	"github.com/sparkwing-dev/sparkwing/internal/githooks"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const sparkwingHookMarker = githooks.Marker

func runHooks(args []string) error {
	if handleParentHelp(cmdHooks, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdHooks, os.Stderr)
		return errors.New("hooks: subcommand required (install|uninstall|status|survey|fire)")
	}
	switch args[0] {
	case "install":
		return runHooksInstall(args[1:])
	case "uninstall":
		return runHooksUninstall(args[1:])
	case "status":
		return runHooksStatus(args[1:])
	case "survey":
		return runHooksSurvey(args[1:])
	case "fire":
		return runHooksFire(args[1:])
	default:
		PrintHelp(cmdHooks, os.Stderr)
		return fmt.Errorf("hooks: unknown subcommand %q", args[0])
	}
}

func runHooksInstall(args []string) error {
	fs := flag.NewFlagSet(cmdHooksInstall.Path, flag.ContinueOnError)
	repo := fs.String("repo", "", "repo directory (default: discovered via .sparkwing/)")
	fleet := fs.Bool("fleet", false, "install into every registered repo")
	noProve := fs.Bool("no-prove", false, "claim core.hooksPath without running the gate first")
	profileName := fs.String("profile", "", "pin the hook's runs to this storage profile (default: whatever the project's config selects)")
	if err := parseAndCheck(cmdHooksInstall, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	opts := installOptions{prove: runPipelineForProof, profile: *profileName}
	if *noProve {
		opts.prove = nil
	}
	if opts.profile != "" {
		if _, err := resolveProfileFlag(opts.profile); err != nil {
			return fmt.Errorf("hooks install: %w", err)
		}
	}
	if *fleet {
		if *repo != "" {
			return errors.New("hooks install: --fleet installs into every registered repo; drop --repo or drop --fleet")
		}
		return installFleet(opts)
	}
	repoRoot, sparkwingDir, err := resolveHooksRepo(*repo)
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	if _, err := installHooks(runGit, repoRoot, sparkwingDir, opts); err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	return nil
}

func installFleet(opts installOptions) error {
	roots, err := fleetRepoRoots(runGit)
	if err != nil {
		return fmt.Errorf("hooks install: %w", err)
	}
	if len(roots) == 0 {
		fmt.Fprintln(os.Stdout, "hooks install: no repos registered; run `sparkwing configure xrepo add <dir>` first")
		return nil
	}
	armed, noGate := 0, 0
	var ungated, failed []string
	for _, root := range roots {
		sparkwingDir := filepath.Join(root, ".sparkwing")
		declared := declaredHookNames(root)
		if len(declared) == 0 {
			noGate++
			continue
		}
		fmt.Fprintf(os.Stdout, "\n=== %s\n", root)
		gated, err := installHooks(runGit, root, sparkwingDir, opts)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stdout, "error: %v\n", err)
			failed = append(failed, filepath.Base(root))
		case gated:
			armed++
		case len(declaredGates(declared)) == 0:
			noGate++
		default:
			ungated = append(ungated, filepath.Base(root))
		}
	}
	fmt.Fprintf(os.Stdout, "\n%d repo(s) armed, %d with no gate to arm, %d left ungated, %d failed\n",
		armed, noGate, len(ungated), len(failed))
	if len(ungated) > 0 {
		fmt.Fprintf(os.Stdout, "still ungated: %s\n"+
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

	profile string
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
	for _, p := range cfg.Pipelines {
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

func declaredHookNames(repoRoot string) []string {
	declared, err := declaredHooks(filepath.Join(repoRoot, ".sparkwing"))
	if err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(declared))
}

func installHooks(git githooks.Git, repoRoot, sparkwingDir string, opts installOptions) (bool, error) {
	hooksToRun, err := declaredHooks(sparkwingDir)
	if err != nil {
		return false, err
	}
	if len(hooksToRun) == 0 {
		fmt.Fprintln(os.Stdout, "hooks install: no pipelines declare pre_commit, pre_push, or post_commit triggers")
		return false, nil
	}

	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return false, err
	}
	globalHooks := chainableGlobalHooks(git, hooksDir)
	globalState, err := captureGlobalHookState(git, hooksDir, globalHooks)
	if err != nil {
		return false, err
	}
	plan, proceed := prepareHookInstall(git, repoRoot, hooksDir,
		prospectiveUnforwardedGlobalHooks(globalHooks, hooksDir), hooksToRun, opts.prove)
	if !proceed {
		return githooks.Survey(git, repoRoot, declaredHookNames(repoRoot)).Gated(), nil
	}
	currentGlobalHooks := chainableGlobalHooks(git, hooksDir)
	currentGlobalState, err := captureGlobalHookState(git, hooksDir, currentGlobalHooks)
	if err != nil {
		return false, err
	}
	if !sameGlobalHookState(globalState, currentGlobalState) {
		return false, errors.New("global hooks changed while hook gates were proving; re-run the install")
	}
	if active, scope := githooks.ActivePath(git, repoRoot); active != plan.active || scope != plan.scope {
		return false, errors.New("core.hooksPath changed while hook gates were proving; re-run the install")
	}
	snapshot, err := captureHookInstallSnapshot(git, repoRoot, hooksDir,
		slices.Concat(slices.Sorted(maps.Keys(hooksToRun)), slices.Sorted(maps.Keys(globalHooks))))
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, errors.Join(err, snapshot.restore(git, repoRoot, hooksDir))
	}

	installed, skipped := 0, 0
	for _, hookName := range slices.Sorted(maps.Keys(hooksToRun)) {
		pipes := hooksToRun[hookName]
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, pipes, globalHooks[hookName], opts.profile))
		if err != nil {
			return false, errors.Join(err, snapshot.restore(git, repoRoot, hooksDir))
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
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, nil, true, opts.profile))
		if err != nil {
			return false, errors.Join(err, snapshot.restore(git, repoRoot, hooksDir))
		}
		if !wrote {
			skipped++
			continue
		}
		fmt.Fprintf(os.Stdout, "installed %s -> the global hook\n", hookName)
		installed++
	}
	fmt.Fprintf(os.Stdout, "\n%d hook(s) installed, %d skipped\n", installed, skipped)
	gated, err := armHooks(git, repoRoot, hooksDir, unforwardedGlobalHooks(globalHooks, hooksDir), hooksToRun, plan)
	if err != nil {
		return false, errors.Join(err, snapshot.restore(git, repoRoot, hooksDir))
	}
	reportSilencedGlobalHooks(git, repoRoot, hooksDir)
	return gated, nil
}

type hookInstallPlan struct {
	active      string
	scope       string
	reads       bool
	unforwarded []string
}

func prepareHookInstall(git githooks.Git, repoRoot, hooksDir string, unforwarded []string, hooksToRun map[string][]string, prove Prover) (hookInstallPlan, bool) {
	active, scope := githooks.ActivePath(git, repoRoot)
	plan := hookInstallPlan{
		active: active, scope: scope, reads: active == "" || githooks.SameDir(active, hooksDir),
		unforwarded: append([]string(nil), unforwarded...),
	}
	if !plan.reads {
		if scope == "local" {
			fmt.Fprintf(os.Stdout, "\nwarning: git reads hooks from %s, not %s, so nothing was installed\n"+
				"  this repo sets its own core.hooksPath, which was deliberate, so the install leaves it alone; clear it with `git -C %s config --unset core.hooksPath`, then re-run `sparkwing pipeline hooks install`\n",
				active, hooksDir, repoRoot)
			return plan, false
		}
		if len(unforwarded) > 0 {
			fmt.Fprintf(os.Stdout, "\nwarning: core.hooksPath left alone: nothing here can hand off to the machine's %s\n"+
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
	fmt.Fprintln(os.Stdout, "\nwarning: installation rejected because a required gate proof failed")
	for _, hookName := range slices.Sorted(maps.Keys(unproven)) {
		fmt.Fprintf(os.Stdout, "  %s: %v\n", hookName, unproven[hookName])
	}
	fmt.Fprintln(os.Stdout, "  prior hooks and core.hooksPath remain unchanged")
	fmt.Fprintln(os.Stdout, "  fix the gate(s), then re-run `sparkwing pipeline hooks install`; `--no-prove` arms them without the proof")
	return plan, false
}

type hookFileSnapshot struct {
	body   []byte
	mode   os.FileMode
	exists bool
}

type hookInstallSnapshot struct {
	files        map[string]hookFileSnapshot
	hooksPath    string
	hooksPathSet bool
	dirExisted   bool
}

func captureHookInstallSnapshot(git githooks.Git, repoRoot, hooksDir string, names []string) (hookInstallSnapshot, error) {
	s := hookInstallSnapshot{files: map[string]hookFileSnapshot{}}
	if info, err := os.Stat(hooksDir); err == nil && info.IsDir() {
		s.dirExisted = true
	} else if err != nil && !os.IsNotExist(err) {
		return s, err
	}
	for _, name := range names {
		if _, ok := s.files[name]; ok {
			continue
		}
		path := filepath.Join(hooksDir, name)
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			s.files[name] = hookFileSnapshot{}
			continue
		}
		if err != nil {
			return s, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return s, err
		}
		s.files[name] = hookFileSnapshot{body: body, mode: info.Mode(), exists: true}
	}
	s.hooksPath, s.hooksPathSet = localHooksPathValue(git, repoRoot)
	return s, nil
}

func (s hookInstallSnapshot) restore(git githooks.Git, repoRoot, hooksDir string) error {
	var errs []error
	for name, prior := range s.files {
		path := filepath.Join(hooksDir, name)
		if !prior.exists {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			continue
		}
		if err := replaceHookFile(path, prior.body, prior.mode.Perm()); err != nil {
			errs = append(errs, err)
		}
	}
	current, set := localHooksPathValue(git, repoRoot)
	switch {
	case s.hooksPathSet && (!set || current != s.hooksPath):
		if _, err := git(repoRoot, "config", "core.hooksPath", s.hooksPath); err != nil {
			errs = append(errs, err)
		}
	case !s.hooksPathSet && set:
		if _, err := git(repoRoot, "config", "--unset", "core.hooksPath"); err != nil {
			errs = append(errs, err)
		}
	}
	if !s.dirExisted {
		if err := os.Remove(hooksDir); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func localHooksPathValue(git githooks.Git, repoRoot string) (string, bool) {
	out, err := git(repoRoot, "config", "--local", "core.hooksPath")
	if err != nil {
		return "", false
	}
	return strings.TrimSuffix(out, "\n"), true
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

func writeManagedHook(hooksDir, name, content string) (bool, error) {
	path := filepath.Join(hooksDir, name)
	if existing, err := os.ReadFile(path); err == nil && !strings.Contains(string(existing), sparkwingHookMarker) {
		fmt.Fprintf(os.Stdout, "skipped %s: existing hook is not managed by sparkwing (remove it first)\n", name)
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

type globalHookFileState struct {
	fileType   os.FileMode
	linkTarget string
	stat       os.FileInfo
	body       [sha256.Size]byte
	mode       os.FileMode
}

type globalHookState struct {
	path  string
	files map[string]globalHookFileState
}

func captureGlobalHookState(git githooks.Git, hooksDir string, hooks map[string]bool) (globalHookState, error) {
	dir := githooks.GlobalPath(git)
	state := globalHookState{path: canonicalHookPath(dir), files: make(map[string]globalHookFileState, len(hooks))}
	if dir == "" || githooks.SameDir(dir, hooksDir) {
		return state, nil
	}
	for _, name := range slices.Sorted(maps.Keys(hooks)) {
		path := filepath.Join(dir, name)
		linfo, err := os.Lstat(path)
		if err != nil {
			return globalHookState{}, err
		}
		var linkTarget string
		if linfo.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return globalHookState{}, err
			}
		}
		info, err := os.Stat(path)
		if err != nil {
			return globalHookState{}, err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return globalHookState{}, err
		}
		state.files[name] = globalHookFileState{
			fileType: linfo.Mode() & os.ModeType, linkTarget: linkTarget,
			stat: info, body: sha256.Sum256(body), mode: info.Mode(),
		}
	}
	return state, nil
}

func canonicalHookPath(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func sameGlobalHookState(before, after globalHookState) bool {
	if before.path != after.path || len(before.files) != len(after.files) {
		return false
	}
	for name, first := range before.files {
		second, ok := after.files[name]
		if !ok || first.fileType != second.fileType || first.linkTarget != second.linkTarget ||
			!os.SameFile(first.stat, second.stat) || first.body != second.body || first.mode != second.mode {
			return false
		}
	}
	return true
}

func armHooks(git githooks.Git, repoRoot, hooksDir string, unforwarded []string, hooksToRun map[string][]string, plan hookInstallPlan) (bool, error) {
	if !slices.Equal(unforwarded, plan.unforwarded) {
		return false, fmt.Errorf("global hook forwarding changed while hooks were being installed: %s", strings.Join(unforwarded, ", "))
	}
	gates := declaredGates(slices.Sorted(maps.Keys(hooksToRun)))
	if len(githooks.Gates(hooksDir)) == 0 {
		fmt.Fprintf(os.Stdout, "\nwarning: no gate runs in %s, so this repo's commits stay ungated\n", hooksDir)
		return false, nil
	}
	if !plan.reads {
		if _, err := git(repoRoot, "config", "core.hooksPath", hooksDir); err != nil {
			return false, fmt.Errorf("claim core.hooksPath for this repo: %w", err)
		}
		fmt.Fprintf(os.Stdout, "\ncore.hooksPath -> %s\n"+
			"  the global core.hooksPath (%s) would otherwise shadow these hooks; its own hooks still fire, chained after this repo's\n",
			hooksDir, plan.active)
	}
	return gatesLive(hooksDir, gates), nil
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
			fmt.Fprintf(os.Stdout, "\nproving %s (%s) before arming it...\n", pipeline, hookName)
			if err := prove(repoRoot, pipeline); err != nil {
				failed[hookName] = fmt.Errorf("%s did not pass: %w", pipeline, err)
				break
			}
			fmt.Fprintf(os.Stdout, "  %s passed\n", pipeline)
		}
	}
	return failed
}

func runPipelineForProof(repoRoot, pipeline string) error {
	cmd := exec.Command("sparkwing", "run", pipeline)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "SPARKWING_LOG_FORMAT=quiet")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s", err, lastLine(out))
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

func statusHooks(git githooks.Git, repoRoot string) error {
	hooksDir, err := githooks.Dir(repoRoot)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		entries = nil
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
	}
	survey := githooks.Survey(git, repoRoot, declaredHookNames(repoRoot))
	if len(survey.NotFiring()) > 0 || len(survey.Borrowed) > 0 {
		fmt.Fprintf(os.Stdout, "\nwarning: %s\n  %s\n", survey.Summary(), survey.Remedy())
	}
	reportSilencedGlobalHooks(git, repoRoot, hooksDir)
	return nil
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
	fmt.Fprintf(os.Stdout, "\nwarning: the machine's global %s does not fire here: git reads %s and nothing there hands off to it\n"+
		"  re-run `sparkwing pipeline hooks install` to restore the forwarder, clearing any hand-written hook of that name first -- install will not overwrite one\n",
		strings.Join(silenced, ", "), hooksDir)
}

func runHooksSurvey(args []string) error {
	fs := flag.NewFlagSet(cmdHooksSurvey.Path, flag.ContinueOnError)
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	ungatedOnly := fs.Bool("ungated", false, "list only the repos git runs no gate for")
	if err := parseAndCheck(cmdHooksSurvey, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveTTYAwareOutput(*outFmt, cmdHooksSurvey.Path)
	if err != nil {
		return err
	}
	rows, err := surveyFleet(runGit)
	if err != nil {
		return fmt.Errorf("hooks survey: %w", err)
	}
	if *ungatedOnly {
		rows = githooks.Ungated(rows)
	}
	return renderHooksSurvey(os.Stdout, rows, format)
}

func surveyFleet(git githooks.Git) ([]githooks.RepoGates, error) {
	roots, err := fleetRepoRoots(repos.Git(git))
	if err != nil {
		return nil, err
	}
	return githooks.SurveyFleet(git, roots, declaredHookNames), nil
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
		fmt.Fprintln(w, "no repos registered; run `sparkwing configure xrepo add <dir>` first")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REPO\tSTATE\tDECLARED\tNOT FIRING\tBORROWED")
	ungated := 0
	for _, r := range rows {
		if !r.Gated() {
			ungated++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", filepath.Base(r.Repo), r.State,
			joinOrDash(r.Declared), joinOrDash(r.NotFiring()), joinOrDash(r.Borrowed))
	}
	_ = tw.Flush()
	if ungated == 0 {
		fmt.Fprintf(w, "\n%d repo(s), every declared gate fires\n", len(rows))
		fmt.Fprintln(w, "this is what the hook directories say; `sparkwing pipeline hooks fire --fleet` is what a commit says")
		return nil
	}
	fmt.Fprintf(w, "\n%d of %d repo(s) do not run a gate of their own:\n", ungated, len(rows))
	for _, r := range rows {
		if r.Gated() {
			continue
		}
		fmt.Fprintf(w, "  %s\n    %s\n", r.Summary(), r.Remedy())
	}
	return nil
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

func renderHookScript(hookName string, pipes []string, chainGlobal bool, profileName string) string {
	blocking := hookName != "post-commit"
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# " + sparkwingHookMarker + " -- do not edit; use `sparkwing pipeline hooks (un)install`\n")
	if blocking && len(pipes) > 0 {
		b.WriteString(githooks.SelfTestScript())
	}
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
	if len(pipes) > 0 {
		b.WriteString("(\n")
		b.WriteString(gitenv.ShellUnbind())

		b.WriteString("unset SPARKWING_PROFILE\n")
		flag := ""
		if profileName != "" {
			flag = " --profile " + shellSingleQuote(profileName)
		}
		for _, p := range pipes {
			// safety: pipeline names come from repo config, so quote before they reach sh.
			fmt.Fprintf(&b, "sparkwing run %s%s%s%s\n", shellSingleQuote(p), flag, stdin, tolerate)
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
