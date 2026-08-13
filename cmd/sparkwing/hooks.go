// `sparkwing pipeline hooks` subcommand. Installs, uninstalls, and reports on
// git hook scripts that fire sparkwing pipelines on commit / push.
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
	if err := parseAndCheck(cmdHooksInstall, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	opts := installOptions{prove: runPipelineForProof}
	if *noProve {
		opts.prove = nil
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

// installFleet runs the install in every repo the machine has registered.
//
// The sweep enumerates the registry rather than taking a list of repositories
// from its caller, so a checkout registered after this code was written is
// swept without anyone remembering it exists -- the failure that left single
// repositories ungated for weeks each time a sweep was scoped by hand.
//
// One repository's failure does not stop the others: a sweep that aborts
// partway leaves exactly the silent, partial coverage it was run to end.
//
// The summary counts what each install left behind rather than that it
// returned without an error. An install that proves a repository's gates,
// finds none of them able to run and therefore arms nothing is a complete,
// unexceptional run -- counting it as armed would report a swept fleet as
// gated while every commit in it still goes ungated. A repository that
// declares no hook git can refuse work with is counted apart for the same
// reason, and is not a repository the sweep left ungated: there is nothing
// there for a re-run to arm.
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

// fleetRepoRoots enumerates the machine's sparkwing checkouts, canonicalized
// to primary repositories so a linked worktree does not become a row of its
// own -- git keeps one hook directory per repository, and arming it twice
// through two paths is the same claim made twice.
//
// A registry it could not read is an error, never an empty fleet. The two
// answers render identically once the list is empty -- no rows, no ungated
// repos, nothing to install -- so a corrupt repos.yaml reported a swept,
// clean machine while every gate question went unasked. Whoever holds the
// list decides which it was, because by the time the caller has a slice the
// evidence is gone. Load treats a *missing* file as an empty config on
// purpose, and that stays: a laptop that has registered nothing really has
// an empty fleet.
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

// Prover runs one of repoRoot's pipelines the way an installed hook will. An
// install takes it as a dependency so the proof can be faked in tests and
// skipped by an operator who has already made it.
type Prover func(repoRoot, pipeline string) error

// installOptions carry what an install needs beyond the repository itself.
type installOptions struct {
	// prove runs a blocking gate before core.hooksPath is claimed. Nil arms
	// the repository without the proof.
	prove Prover
}

// declaredHooks maps every git hook name repoRoot's pipelines ask for to the
// pipelines that asked for it.
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

// declaredHookNames returns the hook names repoRoot asks git to run, sorted.
// A project that cannot be read declares nothing, so a survey still reports
// the repository instead of dropping it.
func declaredHookNames(repoRoot string) []string {
	declared, err := declaredHooks(filepath.Join(repoRoot, ".sparkwing"))
	if err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(declared))
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
//
// It reports whether git ends up running a gate for every one the repository
// declares, which is not the same question as whether the install failed:
// proving a repository's gates and finding none of them able to run is a
// complete run that arms nothing. A repository that declares no gate reports
// false too -- there was never one to arm, and a commit in it goes unchecked
// whatever this run did.
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
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, pipes, globalHooks[hookName]))
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
		wrote, err := writeManagedHook(hooksDir, hookName, renderHookScript(hookName, nil, true))
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

// armHooks makes the hooks just written the ones git runs for this
// repository, and reports whether git ends up running a gate for every one
// the repository declares.
//
// Two things stand between a written hook and a firing one. A core.hooksPath
// override sends git to another directory, which the install claims away when
// the machine set it -- an override the repository itself carries was set
// deliberately, so it is reported instead. unforwarded names the machine's
// hooks nothing in hooksDir hands off to; claiming the path while any remain
// would trade one silent gate for another, so the claim is refused and the
// hook to clear is named.
//
// armHooks claims the repository hook path after every candidate hook has
// been published. Proof and feasibility checks have already passed.
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

// declaredGates returns the hooks among hookNames that can refuse work, in
// the order they are proven.
//
// It is what the arming path counts rather than every declared hook: only a
// blocking hook is ever proven, so a repository that also declares
// post-commit could never fail as many proofs as it declares hooks, and read
// as having something left to arm with every gate it owns red.
func declaredGates(hookNames []string) []string {
	var gates []string
	for _, name := range githooks.BlockingHooks {
		if slices.Contains(hookNames, name) {
			gates = append(gates, name)
		}
	}
	return gates
}

// gatesLive reports whether hooksDir -- by now the directory git reads for
// this repository -- holds a managed hook for every gate in declared.
//
// It answers [githooks.RepoGates.Gated]'s question of a directory an install
// has just written rather than of a survey, so a sweep and a survey say the
// same thing about the same repository. They part on one that declares no
// gate: the survey has nothing missing to report, while the install armed
// nothing and says so rather than let a fleet summary count it among the
// repositories a gate now fires in.
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

// proveGates runs each blocking gate once, before the hooks that run it can
// fire, and returns the hook names whose gate did not pass.
//
// While a repository's hooks are inert a gate that cannot execute at all --
// an admission daemon it cannot speak to, a pipeline red on the default
// branch -- is indistinguishable from a gate that passes. Arming converts the
// first case into a commit that fails every time, which is worse than the
// silence it replaces: an ungated repository still accepts work. Running the
// gate first is what separates the two, and it costs one run per install of a
// repository that is about to gate its own commits.
//
// Every blocking hook is proven even after one fails, so the caller can arm
// the gates that work rather than the whole repository or none of it.
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

// runPipelineForProof runs a pipeline the way the installed hook will: the
// `sparkwing` on PATH, in the repository being armed. Reusing the hook's own
// command is what makes the proof cover the binary that will actually run,
// which for a pipeline built from the repository's own SDK pin is not the
// process making the install.
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

// lastLine is the final non-empty line of a command's output, which for a
// failed pipeline run is the message worth repeating.
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

// runHooksSurvey reports what git gates in every registered repository.
//
// A survey that could not read the registry writes nothing and exits
// non-zero. Rendering an empty fleet would print "no repos registered", or
// `[]` under -o json, which is what a genuinely clean machine prints, and a
// reader who believes that stops looking. The error is the only output that
// cannot be mistaken for an answer.
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

// surveyFleet classifies every registered repository's gates. It is the one
// place the fleet is enumerated, so `survey`, `install --fleet` and `doctor`
// all answer for the same set of repositories.
//
// It returns the registry's error rather than an empty survey, so every one
// of those three has to decide what to say when it could not look.
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
		// NDJSON: one repo per line, so `head` returns whole repos. An
		// empty fleet is an empty stream.
		return ndjson.Write(w, rows)
	case "plain":
		// One tab-separated row per repo, no summary and no alignment, so
		// `cut` and `awk` get a stable shape whatever the fleet looks like.
		// The column count stays put: a borrowed gate is already named by the
		// state, and widening a shape `cut` reads would break the readers this
		// format exists for.
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
//
// A blocking hook that runs pipelines opens with the self-test guard, so
// `sparkwing pipeline hooks fire` can see it refuse a commit without paying
// for the gate. Nothing about a hook that only forwards is worth proving --
// it gates nothing -- and the guard can only refuse, never allow, so it adds
// no way past a gate.
//
// The pipelines run in a subshell that drops the repository-binding GIT_*
// variables git hands every hook, keeping the index git is composing the
// commit in as SPARKWING_GATE_INDEX, so a step that runs git elsewhere is not
// silently redirected at the repository being gated and a step that wants what
// is being committed can still ask for it. sparkwing unbinds itself too; doing
// it here as well covers the sparkwing on PATH being older than the install
// that wrote this file. The subshell is what keeps the scrub off the hand-off:
// the global hook is git's to configure, and it gets the environment git meant
// it to have.
func renderHookScript(hookName string, pipes []string, chainGlobal bool) string {
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
		for _, p := range pipes {
			fmt.Fprintf(&b, "sparkwing run %s%s%s\n", p, stdin, tolerate)
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
