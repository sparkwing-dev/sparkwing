// `sparkwing repos {list,info,update}` -- the machine's fleet of
// sparkwing-bearing checkouts, a single-repo deep dive, and a one-command
// SDK-pin bump with a compiled per-repo verdict. See internal/repos for the
// derivation, plan-diff, and verdict-ladder logic; this file owns flag
// parsing, the side-effecting Ops implementation, and rendering.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/docs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// runRepos dispatches `sparkwing repos <verb>`. Bare `repos` lists, the same
// as the explicit `list` verb.
func runRepos(args []string) error {
	if handleParentHelp(cmdRepos, args) {
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "update":
			return runReposUpdate(args[1:])
		case "list":
			return runReposList(args[1:])
		case "info":
			return runReposInfo(args[1:])
		}
	}
	return runReposList(args)
}

// fleetGit adapts runGit to the repos.Git dependency.
func fleetGit(dir string, args ...string) (string, error) {
	return runGit(dir, args...)
}

// guideVersions returns the versions of every embedded migration guide,
// used to count how many a repo is behind.
func guideVersions() []string {
	entries := docs.MigrationsList()
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Version)
	}
	return out
}

// guidesFor maps the crossed migration-guide range into verdict guides.
func guidesFor(from, to string) []repos.Guide {
	entries, err := docs.MigrationsBetween(from, to)
	if err != nil {
		return nil
	}
	out := make([]repos.Guide, 0, len(entries))
	for _, e := range entries {
		out = append(out, repos.Guide{Version: e.Version, Title: e.Title, Summary: e.Summary})
	}
	return out
}

// buildFleet derives the fleet from repos.yaml candidates and observed
// runs in the local store. latest drives the guides-behind count.
func buildFleet(latest string) ([]repos.Repo, error) {
	cands, err := repos.CandidatePaths()
	if err != nil {
		return nil, err
	}
	obs := observeRuns()
	gv := guideVersions()
	fleet := repos.DeriveFleet(cands, obs, fleetGit, latest, func(pin, lt string) int {
		return repos.GuidesBehind(gv, pin, lt)
	})
	return fleet, nil
}

// observeRuns projects recent runs into repo observations. A store
// that can't be opened (fresh laptop, schema skew) yields no
// observations rather than failing the fleet listing -- repos.yaml
// alone still produces a useful fleet.
func observeRuns() []repos.RunObservation {
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return nil
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()
	runs, err := st.ListRuns(context.Background(), store.RunFilter{Limit: 500})
	if err != nil {
		return nil
	}
	out := make([]repos.RunObservation, 0, len(runs))
	for _, r := range runs {
		if r.Repo == "" {
			continue
		}
		out = append(out, repos.RunObservation{
			Repo:     r.Repo,
			RepoURL:  r.RepoURL,
			Pipeline: r.Pipeline,
			At:       r.StartedAt,
		})
	}
	return out
}

func runReposList(args []string) error {
	fs := flag.NewFlagSet(cmdRepos.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json | plain")
	if err := parseAndCheck(cmdRepos, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("repos: unexpected positional %q", fs.Arg(0))
	}

	latest, _ := fetchLatestRelease()
	fleet, err := buildFleet(latest)
	if err != nil {
		return err
	}

	switch strings.ToLower(output) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(fleetJSON(fleet, latest))
	case "plain":
		for _, r := range fleet {
			fmt.Printf("%s\t%s\t%s\n", r.Name, orDashStr(r.Pin), orDashStr(r.Primary))
		}
		return nil
	}
	printFleet(fleet, latest)
	return nil
}

// fleetRepoJSON is the wire shape of one fleet row.
type fleetRepoJSON struct {
	Name         string              `json:"name"`
	Primary      string              `json:"primary,omitempty"`
	Pin          string              `json:"pin,omitempty"`
	Replace      string              `json:"replace,omitempty"`
	Latest       string              `json:"latest,omitempty"`
	GuidesBehind int                 `json:"guides_behind"`
	LastRun      string              `json:"last_run,omitempty"`
	LastPipeline string              `json:"last_pipeline,omitempty"`
	Status       string              `json:"status"`
	Worktrees    []fleetWorktreeJSON `json:"worktrees,omitempty"`
}

type fleetWorktreeJSON struct {
	Path string `json:"path"`
	Pin  string `json:"pin,omitempty"`
}

func fleetJSON(fleet []repos.Repo, latest string) []fleetRepoJSON {
	out := make([]fleetRepoJSON, 0, len(fleet))
	for _, r := range fleet {
		row := fleetRepoJSON{
			Name:         r.Name,
			Primary:      r.Primary,
			Pin:          r.Pin,
			Replace:      r.Replace,
			Latest:       latest,
			GuidesBehind: r.GuidesBehind,
			LastPipeline: r.LastPipeline,
			Status:       r.Status,
		}
		if !r.LastRun.IsZero() {
			row.LastRun = r.LastRun.Format(time.RFC3339)
		}
		for _, w := range r.DivergentWorktrees() {
			row.Worktrees = append(row.Worktrees, fleetWorktreeJSON{Path: w.Path, Pin: w.Pin})
		}
		out = append(out, row)
	}
	return out
}

func printFleet(fleet []repos.Repo, latest string) {
	if len(fleet) == 0 {
		fmt.Println(color.Dim("no sparkwing repos found (run a pipeline or add one to ~/.config/sparkwing/repos.yaml)"))
		return
	}
	fmt.Printf("Fleet: %d repo(s)", len(fleet))
	if latest != "" {
		fmt.Printf("   latest SDK: %s", latest)
	}
	fmt.Println()
	if repos.PinsDiverge(fleet) {
		fmt.Println(color.Yellow("  pins diverge across the fleet; a shared state schema wants them bumped together (sparkwing repos update)"))
	}
	for _, r := range fleet {
		pin := orDashStr(r.Pin)
		if r.Replace != "" {
			pin = "replaced"
		}
		behind := ""
		if r.GuidesBehind > 0 {
			behind = color.Yellow(fmt.Sprintf(" (%d guide(s) behind)", r.GuidesBehind))
		}
		last := "never"
		if !r.LastRun.IsZero() {
			last = r.LastRun.Format("2006-01-02")
			if r.LastPipeline != "" {
				last += " " + r.LastPipeline
			}
		}
		fmt.Printf("  %-24s %-14s%s  last-run: %s\n", r.Name, pin, behind, last)
		if r.Status == "runs-only" {
			fmt.Printf("      %s\n", color.Dim("observed in runs; no local checkout registered"))
		} else if r.Primary != "" {
			fmt.Printf("      %s\n", color.Dim(r.Primary))
		}
		for _, w := range r.DivergentWorktrees() {
			fmt.Printf("      %s\n", color.Dim(fmt.Sprintf("worktree pins %s: %s", orDashStr(w.Pin), w.Path)))
		}
	}
}

func runReposUpdate(args []string) error {
	fs := flag.NewFlagSet(cmdReposUpdate.Path, flag.ContinueOnError)
	var version, repo, output string
	var apply, verify bool
	fs.StringVar(&version, "version", "", "target SDK release (e.g. v0.16.0); default latest")
	fs.StringVar(&repo, "repo", "", "scope to one repo by name or checkout path")
	fs.BoolVar(&apply, "apply", false, "write the bumps and commit per repo")
	fs.BoolVar(&verify, "verify", false, "run each repo's pre-commit gate after the bump")
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	if err := parseAndCheck(cmdReposUpdate, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("repos update: unexpected positional %q", fs.Arg(0))
	}

	target := strings.TrimSpace(version)
	if target == "" {
		latest, err := fetchLatestRelease()
		if err != nil {
			return fmt.Errorf("repos update: no --version given and could not fetch latest release: %w", err)
		}
		target = latest
	}

	fleet, err := buildFleet(target)
	if err != nil {
		return err
	}
	if repo != "" {
		fleet = scopeFleet(fleet, repo)
		if len(fleet) == 0 {
			return fmt.Errorf("repos update: no tracked repo matches %q", repo)
		}
	}

	cfg := repos.UpdateConfig{
		Target:    target,
		Apply:     apply,
		Verify:    verify,
		GuidesFor: guidesFor,
	}
	verdicts := repos.UpdateFleet(&execOps{}, fleet, cfg)

	if strings.ToLower(output) == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(verdicts)
	}
	printVerdicts(verdicts, fleet, cfg)
	if brokenCount(verdicts) > 0 {
		return exitErrorf(1, "%d repo(s) broke on the bump", brokenCount(verdicts))
	}
	return nil
}

func scopeFleet(fleet []repos.Repo, match string) []repos.Repo {
	abs, _ := filepath.Abs(match)
	var out []repos.Repo
	for _, r := range fleet {
		if strings.EqualFold(r.Name, match) || r.Primary == match || (abs != "" && r.Primary == abs) {
			out = append(out, r)
		}
	}
	return out
}

func brokenCount(vs []repos.Verdict) int {
	n := 0
	for _, v := range vs {
		if v.Kind == repos.VerdictBroken {
			n++
		}
	}
	return n
}

func printVerdicts(vs []repos.Verdict, fleet []repos.Repo, cfg repos.UpdateConfig) {
	mode := "dry run"
	if cfg.Apply {
		mode = "apply"
	}
	fmt.Printf("Bumping %d repo(s) to %s (%s)\n", len(vs), cfg.Target, mode)
	if repos.PinsDiverge(fleet) {
		fmt.Println(color.Yellow("  pins diverge; the shared state schema wants the whole fleet on one pin -- bump them together"))
	}
	for _, v := range vs {
		printOneVerdict(v)
	}
	fmt.Println()
	fmt.Printf("Summary: %s\n", repos.SummarizeVerdicts(vs))
	if !cfg.Apply && anyActionable(vs) {
		fmt.Println(color.Dim("re-run with --apply to write and commit the bumps"))
	}
}

func anyActionable(vs []repos.Verdict) bool {
	for _, v := range vs {
		if v.Kind == repos.VerdictClean || v.Kind == repos.VerdictPlanDiffers {
			return true
		}
	}
	return false
}

func printOneVerdict(v repos.Verdict) {
	var label string
	switch v.Kind {
	case repos.VerdictClean:
		label = color.Green("clean")
	case repos.VerdictPlanDiffers:
		label = color.Yellow("plan-differs")
	case repos.VerdictBroken:
		label = color.Red("broken")
	case repos.VerdictUpToDate:
		label = color.Dim("up-to-date")
	default:
		label = color.Dim(string(v.Kind))
	}
	pins := ""
	if v.FromPin != "" {
		pins = fmt.Sprintf(" %s -> %s", v.FromPin, orDashStr(v.ToPin))
	}
	committed := ""
	if v.Committed {
		committed = color.Dim(" [committed]")
	}
	fmt.Printf("  %-24s %s%s%s\n", v.Repo, label, pins, committed)
	if v.Detail != "" {
		fmt.Printf("      %s\n", color.Dim(v.Detail))
	}
	if v.Err != "" {
		fmt.Printf("      %s\n", color.Red(v.Err))
	}
	for _, pd := range v.Diffs {
		for _, line := range repos.RenderDiff(pd) {
			fmt.Printf("    %s\n", line)
		}
	}
	if len(v.Guides) > 0 {
		fmt.Printf("      %s\n", color.Dim("migration guides in range:"))
		for _, g := range v.Guides {
			title := g.Title
			if title == "" {
				title = g.Summary
			}
			fmt.Printf("        %s  %s\n", g.Version, color.Dim(title))
		}
	}
}

// execOps is the production Ops implementation: git for tree state and
// commits, the go toolchain for the pin bump, and the sparkwing CLI's
// own plan verb for before/after plan construction.
type execOps struct{}

func (execOps) Dirty(dir string) (bool, error) {
	out, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (execOps) Pin(sparkwingDir string) (pin, replace string) {
	return repos.SDKPin(sparkwingDir)
}

func (execOps) Pipelines(sparkwingDir string) ([]string, error) {
	return repos.PipelineNamesForRepo(filepath.Dir(sparkwingDir))
}

func (execOps) Plan(sparkwingDir, pipeline string) (repos.Plan, error) {
	self, err := os.Executable()
	if err != nil {
		self = "sparkwing"
	}
	cmd := exec.Command(self, "pipeline", "plan", "--name", pipeline, "-o", "json")
	cmd.Dir = filepath.Dir(sparkwingDir)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return repos.Plan{}, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parsePlanDoc(pipeline, stdout.Bytes())
}

func (execOps) Snapshot(sparkwingDir string) ([]byte, error) {
	snap := modSnapshot{}
	if b, err := os.ReadFile(filepath.Join(sparkwingDir, "go.mod")); err == nil {
		snap.GoMod = b
	} else {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	if b, err := os.ReadFile(filepath.Join(sparkwingDir, "go.sum")); err == nil {
		snap.GoSum = b
		snap.HadSum = true
	}
	return json.Marshal(snap)
}

func (execOps) Restore(sparkwingDir string, raw []byte) error {
	var snap modSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.mod"), snap.GoMod, 0o644); err != nil {
		return err
	}
	sumPath := filepath.Join(sparkwingDir, "go.sum")
	if snap.HadSum {
		return os.WriteFile(sumPath, snap.GoSum, 0o644)
	}
	if _, err := os.Stat(sumPath); err == nil {
		return os.Remove(sumPath)
	}
	return nil
}

func (execOps) Bump(sparkwingDir, version string) error {
	target := "github.com/sparkwing-dev/sparkwing@" + version
	if out, err := runGoModCmd(sparkwingDir, "get", target); err != nil {
		return fmt.Errorf("go get %s: %v: %s", target, err, out)
	}
	if out, err := runGoModCmd(sparkwingDir, "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %v: %s", err, out)
	}
	return nil
}

func (execOps) Verify(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".pre-commit-config.yaml")); err != nil {
		return fmt.Errorf("no cheap gate found (add a .pre-commit-config.yaml or drop --verify)")
	}
	if _, err := exec.LookPath("pre-commit"); err != nil {
		return fmt.Errorf("pre-commit not on PATH; install it or drop --verify")
	}
	cmd := exec.Command("pre-commit", "run", "--all-files")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pre-commit failed: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

func (execOps) Commit(dir, message string) error {
	if _, err := runGit(dir, "add", ".sparkwing/go.mod", ".sparkwing/go.sum"); err != nil {
		return err
	}
	if _, err := runGit(dir, "commit", "-m", message); err != nil {
		return err
	}
	return nil
}

// modSnapshot is the dry-run restore payload: the .sparkwing go.mod and
// go.sum bytes captured before a bump.
type modSnapshot struct {
	GoMod  []byte `json:"go_mod"`
	GoSum  []byte `json:"go_sum"`
	HadSum bool   `json:"had_sum"`
}

// runGoModCmd runs a go subcommand in dir and returns combined output.
func runGoModCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// parsePlanDoc converts the pipeline binary's plan-preview JSON into
// the structural Plan the diff operates on.
func parsePlanDoc(pipeline string, raw []byte) (repos.Plan, error) {
	var doc planPreviewDoc
	if err := json.Unmarshal(bytes.TrimSpace(raw), &doc); err != nil {
		return repos.Plan{}, fmt.Errorf("parse plan JSON: %w", err)
	}
	name := doc.Pipeline
	if name == "" {
		name = pipeline
	}
	plan := repos.Plan{Pipeline: name}
	for _, n := range doc.Nodes {
		pn := repos.PlanNode{
			ID:          n.ID,
			Deps:        n.Deps,
			Decision:    n.Decision,
			IsApproval:  n.IsApproval,
			OnFailureOf: n.OnFailureOf,
		}
		if n.Work != nil {
			for _, s := range n.Work.Steps {
				pn.Steps = append(pn.Steps, repos.PlanStep{ID: s.ID, Needs: s.Needs, Decision: s.Decision})
			}
			for _, s := range n.Work.Spawns {
				pn.Steps = append(pn.Steps, repos.PlanStep{ID: "spawn:" + s.ID, Needs: s.Needs, Decision: s.Decision})
			}
			for _, s := range n.Work.SpawnEach {
				pn.Steps = append(pn.Steps, repos.PlanStep{ID: "spawn_each:" + s.ID, Needs: s.Needs, Decision: s.Decision})
			}
		}
		plan.Nodes = append(plan.Nodes, pn)
	}
	return plan, nil
}
