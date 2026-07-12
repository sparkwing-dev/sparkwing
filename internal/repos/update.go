package repos

import (
	"fmt"
	"strings"
)

// VerdictKind is the per-repo outcome of an update attempt.
type VerdictKind string

const (
	// VerdictClean: the bump compiled and every pipeline's plan is
	// byte-identical before and after -- a guaranteed no-behavior-change
	// upgrade.
	VerdictClean VerdictKind = "clean"
	// VerdictPlanDiffers: the bump compiled but at least one pipeline's
	// plan changed shape; the structured diff is attached.
	VerdictPlanDiffers VerdictKind = "plan-differs"
	// VerdictBroken: the bump failed to apply, compile, or verify; the
	// error and the crossed migration guides are attached.
	VerdictBroken VerdictKind = "broken"
	// VerdictSkippedDirty: the working tree had uncommitted changes.
	VerdictSkippedDirty VerdictKind = "skipped-dirty"
	// VerdictSkippedMissing: no bumpable .sparkwing pin (missing checkout,
	// replace directive, or runs-only observation).
	VerdictSkippedMissing VerdictKind = "skipped-missing"
	// VerdictUpToDate: the pin already matches the target.
	VerdictUpToDate VerdictKind = "up-to-date"
)

// PipelineDiff pairs a pipeline name with its before/after plan diff.
type PipelineDiff struct {
	Pipeline string
	Diff     PlanDiff
}

// Guide is a crossed migration guide surfaced with a verdict: its
// version, title, and one-line summary.
type Guide struct {
	Version string
	Title   string
	Summary string
}

// Verdict is the full result for one repo.
type Verdict struct {
	Repo      string
	Primary   string
	Kind      VerdictKind
	FromPin   string
	ToPin     string
	Err       string
	Diffs     []PipelineDiff
	Guides    []Guide
	Committed bool
	Detail    string
}

// Ops are the side-effecting operations the updater performs against a
// repo, injected so the orchestration is unit-testable without a real
// checkout or toolchain.
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

// UpdateConfig parameterizes a fleet update.
type UpdateConfig struct {
	// Target is the SDK version to bump to (e.g. v0.15.8).
	Target string
	// Apply commits the bump per repo; false is a dry run that restores
	// every repo to its pre-run state.
	Apply bool
	// Verify runs each repo's cheap gate after the bump.
	Verify bool
	// GuidesFor returns the crossed migration guides for a from->to
	// range, used to enrich broken (and green) verdicts.
	GuidesFor func(from, to string) []Guide
}

// UpdateRepo runs the verdict ladder for a single repo directory. The
// sparkwingDir is dir/.sparkwing. It never leaves a dry run's tree
// dirty and never commits a broken bump.
func UpdateRepo(ops Ops, dir, name string, cfg UpdateConfig) Verdict {
	v := Verdict{Repo: name, Primary: dir, ToPin: cfg.Target}
	sparkwingDir := dir + "/.sparkwing"

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
		_ = ops.Restore(sparkwingDir, snap)
	}

	pipelines, err := ops.Pipelines(sparkwingDir)
	if err != nil {
		restore()
		return broken(v, "list pipelines: "+err.Error(), cfg)
	}

	before := map[string]Plan{}
	for _, p := range pipelines {
		plan, perr := ops.Plan(sparkwingDir, p)
		if perr != nil {
			restore()
			return broken(v, "plan "+p+" (before bump): "+perr.Error(), cfg)
		}
		before[p] = plan
	}

	if err := ops.Bump(sparkwingDir, cfg.Target); err != nil {
		restore()
		return broken(v, "bump pin: "+err.Error(), cfg)
	}

	var diffs []PipelineDiff
	allIdentical := true
	for _, p := range pipelines {
		after, perr := ops.Plan(sparkwingDir, p)
		if perr != nil {
			restore()
			return broken(v, "plan "+p+" (after bump): "+perr.Error(), cfg)
		}
		diff := DiffPlans(before[p], after)
		if !diff.Identical {
			allIdentical = false
		}
		diffs = append(diffs, PipelineDiff{Pipeline: p, Diff: diff})
	}

	if cfg.Verify {
		if err := ops.Verify(dir); err != nil {
			restore()
			return broken(v, "verify: "+err.Error(), cfg)
		}
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
		if err := ops.Commit(dir, commitMessage(cfg.Target)); err != nil {
			restore()
			return broken(v, "commit: "+err.Error(), cfg)
		}
		v.Committed = true
	} else {
		restore()
	}
	return v
}

// UpdateFleet runs UpdateRepo across a set of repos and returns the
// verdicts in input order.
func UpdateFleet(ops Ops, repos []Repo, cfg UpdateConfig) []Verdict {
	out := make([]Verdict, 0, len(repos))
	for _, r := range repos {
		if r.Primary == "" {
			out = append(out, Verdict{
				Repo: r.Name, Kind: VerdictSkippedMissing,
				Detail: "observed in runs but no local checkout registered",
			})
			continue
		}
		out = append(out, UpdateRepo(ops, r.Primary, r.Name, cfg))
	}
	return out
}

func broken(v Verdict, msg string, cfg UpdateConfig) Verdict {
	v.Kind = VerdictBroken
	v.Err = msg
	if cfg.GuidesFor != nil && v.FromPin != "" && cfg.Target != "" {
		v.Guides = cfg.GuidesFor(v.FromPin, cfg.Target)
	}
	return v
}

// commitMessage is the conventional-commit subject written per repo
// under --apply.
func commitMessage(target string) string {
	return "chore: bump sparkwing SDK to " + target
}

// PinsDiverge reports whether the fleet's repos don't all share the
// same current pin -- the condition the report leads with, since the
// shared state schema wants every pin bumped in one sitting.
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

// RenderDiff formats a plan diff as indented detail lines.
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

// SummarizeVerdicts returns a one-line tally per kind for the report
// footer, in a stable order.
func SummarizeVerdicts(vs []Verdict) string {
	counts := map[VerdictKind]int{}
	for _, v := range vs {
		counts[v.Kind]++
	}
	order := []VerdictKind{
		VerdictClean, VerdictPlanDiffers, VerdictBroken,
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
