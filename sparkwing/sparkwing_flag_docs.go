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
	{Name: "sw-cd", Short: "C", Argument: "PATH", Desc: "Run as if started in PATH", Group: "System"},
	{Name: "sw-ref", Argument: "REF", Desc: "Run the pipeline at REF (branch/tag/SHA) instead of the working tree", Group: "System", Hot: true},
	{Name: "sw-verbose", Short: "v", Desc: "Enable debug logging", Group: "System"},
	{Name: "sw-start-at", Argument: "STEP", Desc: "Start the run at STEP", Group: "System", Hot: true},
	{Name: "sw-stop-at", Argument: "STEP", Desc: "Stop the run after STEP", Group: "System", Hot: true},
	{Name: "sw-only", Argument: "GLOB", Desc: "Run only jobs whose ID matches GLOB (plus their Needs ancestors)", Group: "System", Hot: true},
	{Name: "sw-no-cache", Desc: "Ignore cached per-node results (writes still happen)", Group: "System", Hot: true},
	{Name: "sw-local-only", Desc: "Force local state, cache, and logs for this run; ignore any configured shared backends", Group: "System"},
	{Name: "sw-dry-run", Desc: "Run each step's dry-run probe instead of its real action", Group: "System", Hot: true},
	{Name: "sw-allow", Argument: "LABEL[,LABEL...]", Desc: "Authorize risk-labeled steps (repeatable)", Group: "System"},
	{Name: "sw-target", Argument: "TARGET", Desc: "Run against the named target (i.e. local, dev, prod)", Group: "System", Hot: true},
	{Name: "sw-profile", Argument: "PROFILE", Desc: "Run via the named profile instead of locally", Group: "System", Hot: true},
}

// SparkwingFlagDocs returns the canonical sparkwing-owned flag
// documentation. The returned slice is a copy; callers may mutate
// freely.
func SparkwingFlagDocs() []SparkwingFlagDoc {
	out := make([]SparkwingFlagDoc, len(sparkwingFlagDocs))
	copy(out, sparkwingFlagDocs)
	return out
}
