package repos

import (
	"context"
	"fmt"
	"strings"
)

type VerdictKind string

const (
	VerdictClean VerdictKind = "clean"

	VerdictPlanDiffers VerdictKind = "plan-differs"

	VerdictBroken VerdictKind = "broken"

	VerdictSkippedDirty VerdictKind = "skipped-dirty"

	VerdictSkippedMissing VerdictKind = "skipped-missing"

	VerdictUpToDate VerdictKind = "up-to-date"

	VerdictInterrupted VerdictKind = "interrupted"
)

// UncomparedPipeline is a pipeline whose baseline plan already failed, so
// the bump could not be judged against it.
type UncomparedPipeline struct {
	Pipeline string
	Reason   string
}

type PipelineDiff struct {
	Pipeline string
	Diff     PlanDiff
}

type Guide struct {
	Version string
	Title   string
	Summary string
}

type Verdict struct {
	Repo       string
	Primary    string
	Kind       VerdictKind
	FromPin    string
	ToPin      string
	Err        string
	Diffs      []PipelineDiff
	Uncompared []UncomparedPipeline
	Guides     []Guide
	Committed  bool
	Detail     string
}

type Ops interface {
	Dirty(dir string) (bool, error)
	Pin(dir string) (pin, replace string)
	Pipelines(dir string) ([]string, error)
	Plan(dir, pipeline string) (Plan, error)
	Snapshot(dir string) ([]byte, error)
	Restore(dir string, snap []byte) error
	Bump(dir, version string) error
	Verify(dir string) error
	Commit(dir, message string) error
}

type UpdateConfig struct {
	Target string

	Apply bool

	Verify bool

	GuidesFor func(from, to string) []Guide

	// Progress receives one line per step. A fleet-wide run compiles every
	// clean repo twice and can hold the report for half an hour, so without
	// this the command looks hung. Nil is silent.
	Progress func(repo, msg string)

	// Context ends the walk early. The repo in flight restores its module
	// files before its verdict comes back, so a Ctrl-C mid-bump does not
	// leave the tree bumped. Nil never ends.
	Context context.Context
}

func (cfg UpdateConfig) ctx() context.Context {
	if cfg.Context == nil {
		return context.Background()
	}
	return cfg.Context
}

func (cfg UpdateConfig) progress(repo, format string, args ...any) {
	if cfg.Progress != nil {
		cfg.Progress(repo, fmt.Sprintf(format, args...))
	}
}

func UpdateRepo(ops Ops, dir, name string, cfg UpdateConfig) Verdict {
	v := Verdict{Repo: name, Primary: dir, ToPin: cfg.Target}
	sparkwingDir := dir + "/.sparkwing"
	ctx := cfg.ctx()

	pin, replace := ops.Pin(sparkwingDir)
	v.FromPin = pin
	if replace != "" {
		v.Kind = VerdictSkippedMissing
		v.Detail = "SDK is replaced with " + replace + "; nothing to bump"
		return v
	}
	if pin == "" {
		v.Kind = VerdictSkippedMissing
		v.Detail = "no SDK pin found in .sparkwing/go.mod"
		return v
	}
	if pin == cfg.Target {
		v.Kind = VerdictUpToDate
		return v
	}

	dirty, err := ops.Dirty(dir)
	if err != nil {
		v.Kind = VerdictSkippedMissing
		v.Detail = "could not read working tree: " + err.Error()
		return v
	}
	if dirty {
		v.Kind = VerdictSkippedDirty
		return v
	}

	// safety: snapshot the pristine module files before any compile.
	// Plan construction itself compiles, which can populate go.sum or add
	// a toolchain line; capturing first lets a dry run restore the tree
	// exactly as it was found.
	snap, err := ops.Snapshot(sparkwingDir)
	if err != nil {
		return broken(v, "snapshot: "+err.Error(), cfg)
	}
	restore := func() {
		if rerr := ops.Restore(sparkwingDir, snap); rerr != nil {
			v.Detail = "restore failed, .sparkwing/go.mod may still carry the bump: " + rerr.Error()
		}
	}
	interrupted := func() Verdict {
		restore()
		v.Kind = VerdictInterrupted
		if v.Detail == "" {
			v.Detail = ".sparkwing module files restored"
		}
		return v
	}
	// A step that died because the walk was interrupted is not evidence
	// against the bump.
	fail := func(msg string) Verdict {
		if ctx.Err() != nil {
			return interrupted()
		}
		restore()
		return broken(v, msg, cfg)
	}

	cfg.progress(name, "listing pipelines")
	pipelines, err := ops.Pipelines(sparkwingDir)
	if err != nil {
		return fail("list pipelines: " + err.Error())
	}

	// A pipeline whose baseline plan already fails -- required inputs, most
	// often -- says nothing about the bump, so it is set aside rather than
	// counted against it.
	before := map[string]Plan{}
	var compared []string
	for i, p := range pipelines {
		if ctx.Err() != nil {
			return interrupted()
		}
		cfg.progress(name, "plan %d/%d %s (before bump)", i+1, len(pipelines), p)
		plan, perr := ops.Plan(sparkwingDir, p)
		if perr != nil {
			if ctx.Err() != nil {
				return interrupted()
			}
			v.Uncompared = append(v.Uncompared, UncomparedPipeline{Pipeline: p, Reason: perr.Error()})
			continue
		}
		before[p] = plan
		compared = append(compared, p)
	}

	cfg.progress(name, "bumping %s -> %s and tidying modules", pin, cfg.Target)
	if err := ops.Bump(sparkwingDir, cfg.Target); err != nil {
		return fail("bump pin: " + err.Error())
	}
	// Compiling against the new pin is the evidence a repo with nothing
	// left to compare still needs.
	cfg.progress(name, "compiling pipelines after bump")
	if _, err := ops.Pipelines(sparkwingDir); err != nil {
		return fail("compile after bump: " + err.Error())
	}

	var diffs []PipelineDiff
	allIdentical := true
	for i, p := range compared {
		if ctx.Err() != nil {
			return interrupted()
		}
		cfg.progress(name, "plan %d/%d %s (after bump)", i+1, len(compared), p)
		after, perr := ops.Plan(sparkwingDir, p)
		if perr != nil {
			return fail("plan " + p + " (after bump): " + perr.Error())
		}
		diff := DiffPlans(before[p], after)
		if !diff.Identical {
			allIdentical = false
		}
		diffs = append(diffs, PipelineDiff{Pipeline: p, Diff: diff})
	}

	if cfg.Verify {
		cfg.progress(name, "running pre-commit gate")
		if err := ops.Verify(dir); err != nil {
			return fail("verify: " + err.Error())
		}
	}
	if ctx.Err() != nil {
		return interrupted()
	}

	v.Diffs = diffs
	if allIdentical {
		v.Kind = VerdictClean
	} else {
		v.Kind = VerdictPlanDiffers
	}
	if cfg.GuidesFor != nil {
		v.Guides = cfg.GuidesFor(pin, cfg.Target)
	}

	if cfg.Apply {
		cfg.progress(name, "committing")
		if err := ops.Commit(dir, commitMessage(cfg.Target)); err != nil {
			return fail("commit: " + err.Error())
		}
		v.Committed = true
	} else {
		restore()
	}
	return v
}

func UpdateFleet(ops Ops, repos []Repo, cfg UpdateConfig) []Verdict {
	out := make([]Verdict, 0, len(repos))
	ctx := cfg.ctx()
	for i, r := range repos {
		if ctx.Err() != nil {
			break
		}
		scoped := cfg
		if cfg.Progress != nil {
			label := fmt.Sprintf("[%d/%d] %s", i+1, len(repos), r.Name)
			scoped.Progress = func(_, msg string) { cfg.Progress(label, msg) }
		}
		var v Verdict
		if r.Primary == "" {
			v = Verdict{
				Repo: r.Name, Kind: VerdictSkippedMissing,
				Detail: "observed in runs but no local checkout registered",
			}
		} else {
			v = UpdateRepo(ops, r.Primary, r.Name, scoped)
		}
		scoped.progress(r.Name, "%s", verdictLine(v))
		out = append(out, v)
	}
	return out
}

func verdictLine(v Verdict) string {
	line := string(v.Kind)
	if v.Kind == VerdictPlanDiffers {
		n := 0
		for _, d := range v.Diffs {
			if !d.Diff.Identical {
				n++
			}
		}
		line += fmt.Sprintf(" (%d pipeline(s) changed shape)", n)
	}
	if len(v.Uncompared) > 0 {
		line += fmt.Sprintf(" (%d pipeline(s) not compared)", len(v.Uncompared))
	}
	if v.Detail != "" {
		line += ": " + v.Detail
	}
	if v.Err != "" {
		line += ": " + v.Err
	}
	return line
}

func broken(v Verdict, msg string, cfg UpdateConfig) Verdict {
	v.Kind = VerdictBroken
	v.Err = msg
	if cfg.GuidesFor != nil && v.FromPin != "" && cfg.Target != "" {
		v.Guides = cfg.GuidesFor(v.FromPin, cfg.Target)
	}
	return v
}

func commitMessage(target string) string {
	return "chore: bump sparkwing SDK to " + target
}

func PinsDiverge(repos []Repo) bool {
	seen := map[string]bool{}
	for _, r := range repos {
		if r.Primary == "" || r.Pin == "" {
			continue
		}
		seen[r.Pin] = true
	}
	return len(seen) > 1
}

func RenderDiff(pd PipelineDiff) []string {
	var out []string
	d := pd.Diff
	for _, id := range d.AddedNodes {
		out = append(out, fmt.Sprintf("  %s: + node %s", pd.Pipeline, id))
	}
	for _, id := range d.RemovedNodes {
		out = append(out, fmt.Sprintf("  %s: - node %s", pd.Pipeline, id))
	}
	for _, nc := range d.ChangedNodes {
		out = append(out, fmt.Sprintf("  %s: ~ node %s (%s)", pd.Pipeline, nc.ID, strings.Join(nc.Details, "; ")))
	}
	return out
}

func SummarizeVerdicts(vs []Verdict) string {
	counts := map[VerdictKind]int{}
	for _, v := range vs {
		counts[v.Kind]++
	}
	order := []VerdictKind{
		VerdictClean, VerdictPlanDiffers, VerdictBroken, VerdictInterrupted,
		VerdictUpToDate, VerdictSkippedDirty, VerdictSkippedMissing,
	}
	var parts []string
	for _, k := range order {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}
	return strings.Join(parts, ", ")
}
