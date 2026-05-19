package sparkwing

// SparkwingFlagDoc is a public, single-source-of-truth description of
// one sparkwing-owned flag. Multiple help-rendering surfaces consume
// this:
//
//   - cmd/sparkwing's `sparkwing run --help` page (FlagSpec entries
//     are derived from this list).
//   - orchestrator's per-pipeline `sparkwing run <pipeline> --help`
//     footer (the "SPARKWING FLAGS" enumeration the user sees
//     alongside PIPELINE FLAGS).
//
// This list is the canonical source: the per-pipeline footer used to
// hand-code "(--on, --from, --config)" and silently drifted every
// time a new flag landed. Sourcing from one list keeps the surfaces
// in lockstep.
//
// Every sparkwing-owned flag is prefixed `sw-`. Pipeline-author
// Inputs flags occupy the full unprefixed namespace; there is no
// reserved-name collision check because none can happen.
type SparkwingFlagDoc struct {
	// Name is the long flag name without the leading "--".
	Name string
	// Short is the optional one-letter alias without the leading "-"
	// (e.g. "v" for --sw-verbose). Empty for flags without a short form.
	Short string
	// Argument is the value placeholder for value-taking flags
	// (e.g. "PATH", "REF"); empty for boolean flags.
	Argument string
	// Desc is the one-line help text shown in --help output.
	Desc string
	// Group is the rendering bucket: currently a single "System"
	// label. Per-pipeline help uses this to section the footer;
	// `sparkwing run --help` uses it via FlagSpec.Group.
	Group string
	// Hot marks flags an operator reaches for on most runs. Default
	// --help and tab-completion menus filter to Hot=true entries to
	// keep the surface small; the long tail surfaces via --help-all.
	Hot bool
}

// sparkwingFlagDocs is the canonical documentation source for
// sparkwing-owned flags. The order here is the order help renderers
// walk; group boundaries determine section breaks. Adding a flag
// here surfaces it in `sparkwing run --help` AND every per-pipeline
// footer simultaneously.
//
// Subsumes `cmd/sparkwing/help_registry.go`'s runFlagSpecs (which
// derives from this list). All sparkwing-owned flags are prefixed
// `sw-` so pipeline authors have the full unprefixed namespace for
// their typed Inputs flags.
var sparkwingFlagDocs = []SparkwingFlagDoc{
	{Name: "sw-change-directory", Short: "C", Argument: "PATH", Desc: "Re-anchor .sparkwing/ discovery to PATH (mirrors `git -C` / `make -C`)", Group: "System"},
	{Name: "sw-from", Argument: "REF", Desc: "Compile from a git ref (branch/tag/SHA) instead of the working tree", Group: "System", Hot: true},
	{Name: "sw-verbose", Short: "v", Desc: "Enable debug logging from the orchestrator (equivalent to SPARKWING_LOG_LEVEL=debug)", Group: "System"},
	{Name: "sw-start-at", Argument: "STEP", Desc: "Start the run at STEP, skipping every step before it", Group: "System", Hot: true},
	{Name: "sw-stop-at", Argument: "STEP", Desc: "Stop the run after STEP, skipping every step beyond it", Group: "System", Hot: true},
	{Name: "sw-dry-run", Desc: "Run each step's dry-run probe instead of its apply Fn; no mutation", Group: "System", Hot: true},
	{Name: "sw-allow", Argument: "LABEL[,LABEL...]", Desc: "Authorize risk-labeled steps (repeatable; comma-separated allowed)", Group: "System"},
	{Name: "sw-for", Argument: "TARGET", Desc: "Pick the pipeline target to run against (Config + Source binding follow)", Group: "System", Hot: true},
	{Name: "sw-job", Argument: "ID=RUNNER", Desc: "Force one job to a specific runner (repeatable; must satisfy that job's Requires)", Group: "System"},
	{Name: "sw-prefer", Argument: "LABEL", Desc: "Bias runner selection by label across the run (repeatable; loses to a job's own Prefers)", Group: "System"},
	{Name: "sw-on", Argument: "NAME", Desc: "Dispatch on a remote controller instead of running locally", Group: "System", Hot: true},
	{Name: "sw-backends-env", Argument: "NAME", Desc: "Force a specific environments: entry from backends.yaml (skips auto-detect)", Group: "System"},
}

// SparkwingFlagDocs returns the canonical sparkwing-owned flag
// documentation. The returned slice is a copy; callers may mutate
// freely.
func SparkwingFlagDocs() []SparkwingFlagDoc {
	out := make([]SparkwingFlagDoc, len(sparkwingFlagDocs))
	copy(out, sparkwingFlagDocs)
	return out
}
