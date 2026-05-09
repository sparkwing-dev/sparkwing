package sparkwing

// WingFlagDoc is a public, single-source-of-truth description of one
// wing-owned flag. Multiple help-rendering surfaces consume this:
//
//   - cmd/sparkwing's `wing --help` and `sparkwing run --help` pages
//     (FlagSpec entries are derived from this list).
//   - orchestrator's per-pipeline `wing <pipeline> --help` footer
//     (the "wing-owned flags" enumeration the user sees alongside
//     PIPELINE FLAGS).
//
// This list is the canonical source: the per-pipeline footer used to
// hand-code "(--on, --from, --config)" and silently drifted every
// time a new wing flag landed. Sourcing from one list keeps the
// surfaces in lockstep.
//
// The flag NAME set is pinned to ReservedFlagNames() via
// TestWingFlagDocsCoverReservedFlags so a future wing flag added to
// reservedFlagNames without a corresponding doc fails the test.
type WingFlagDoc struct {
	// Name is the long flag name without the leading "--".
	Name string
	// Short is the optional one-letter alias without the leading "-"
	// (e.g. "v" for --verbose). Empty for flags without a short form.
	Short string
	// Argument is the value placeholder for value-taking flags
	// (e.g. "PATH", "REF"); empty for boolean flags.
	Argument string
	// Desc is the one-line help text shown in --help output.
	Desc string
	// Group is the rendering bucket: "Source", "Range", "Safety",
	// "System". Per-pipeline help uses this to section the footer;
	// `wing --help` uses it via FlagSpec.Group.
	Group string
}

// wingFlagDocs is the canonical documentation source for wing-owned
// flags. The order here is the order help renderers walk; group
// boundaries determine section breaks. Adding a flag here surfaces it
// in `wing --help`, `sparkwing run --help`, AND every per-pipeline
// footer simultaneously.
//
// Subsumes `cmd/sparkwing/help_registry.go`'s wingFlagSpecs (which
// derives from this list). The reservedFlagNames set is stricter
// (includes infra-only flags like --secrets / --mode / --workers /
// --no-update that are intentionally undocumented in user-facing
// help); WingFlagDocs is the documented subset.
var wingFlagDocs = []WingFlagDoc{
	// Source: where + what to compile.
	{Name: "change-directory", Short: "C", Argument: "PATH", Desc: "Re-anchor .sparkwing/ discovery to PATH (mirrors `git -C` / `make -C`)", Group: "Source"},
	{Name: "from", Argument: "REF", Desc: "Compile from a git ref (branch/tag/SHA) instead of the working tree", Group: "Source"},
	{Name: "config", Argument: "PRESET", Desc: "Apply a named preset from .sparkwing/config.yaml or ~/.config/sparkwing/config.yaml", Group: "Source"},
	{Name: "retry-of", Argument: "RUN_ID", Desc: "Retry a prior run: skip nodes that passed, re-run the rest", Group: "Source"},
	{Name: "full", Desc: "With --retry-of, disable skip-passed so every node re-runs from scratch", Group: "Source"},
	{Name: "verbose", Short: "v", Desc: "Enable debug logging from the orchestrator (equivalent to SPARKWING_LOG_LEVEL=debug)", Group: "Source"},
	// Range: which subset of the DAG runs.
	{Name: "start-at", Argument: "STEP", Desc: "Start the run at STEP, skipping every step before it", Group: "Range"},
	{Name: "stop-at", Argument: "STEP", Desc: "Stop the run after STEP, skipping every step beyond it", Group: "Range"},
	// Safety: blast-radius gates + dry-run.
	{Name: "dry-run", Desc: "Run each step's dry-run probe instead of its apply Fn; no mutation", Group: "Safety"},
	{Name: "allow-destructive", Desc: "Authorize dispatch when the plan reaches a Destructive-marked step", Group: "Safety"},
	{Name: "allow-prod", Desc: "Authorize dispatch when the plan reaches a AffectsProduction-marked step", Group: "Safety"},
	{Name: "allow-money", Desc: "Authorize dispatch when the plan reaches a CostsMoney-marked step", Group: "Safety"},
	// System: where the work runs.
	{Name: "on", Argument: "NAME", Desc: "Dispatch on a remote controller instead of running locally", Group: "System"},
}

// WingFlagDocs returns the canonical wing-owned flag documentation.
// The returned slice is a copy; callers may mutate freely.
func WingFlagDocs() []WingFlagDoc {
	out := make([]WingFlagDoc, len(wingFlagDocs))
	copy(out, wingFlagDocs)
	return out
}
