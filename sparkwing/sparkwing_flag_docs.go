package sparkwing

// SparkwingFlagDoc describes one sparkwing-owned flag; every help surface
// that lists sparkwing flags renders from [SparkwingFlagDocs].
//
// Every sparkwing-owned flag is prefixed `sw-`, leaving the unprefixed
// namespace to pipeline Inputs flags, so no reserved-name check is needed.
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
	// Group is the section `sparkwing run --help` and the command index list
	// the flag under.
	Group string
	// Hot marks flags an operator reaches for on most runs. Default
	// --help and tab-completion menus filter to Hot=true entries to
	// keep the surface small; the long tail surfaces via --help-all.
	Hot bool
}

var sparkwingFlagDocs = []SparkwingFlagDoc{
	{Name: "sw-ref", Argument: "REF", Desc: "Run the pipeline at REF (branch/tag/SHA) instead of the working tree", Group: "System", Hot: true},
	{Name: "sw-ref-worktree", Argument: "NAME", Desc: "Detached only, with --sw-ref: execute in the named worktree, reused across runs so build output Git ignores stays warm; a run holding it makes the next one wait", Group: "System"},
	{Name: "sw-pipeline-ref", Argument: "REF", Desc: "Compile the pipeline at REF and execute it in this checkout; combines with --sw-ref only alongside --sw-ref-worktree", Group: "System"},
	{Name: "sw-detached", Desc: "Queue the run for this machine's resident consumer and print its handle instead of executing here; the run outlives the terminal", Group: "System", Hot: true},
	{Name: "sw-idempotency-key", Argument: "KEY", Desc: "Detached only: deduplication token; a repeat carrying this key returns the original run instead of starting a second one", Group: "System"},
	{Name: "sw-request-id", Argument: "ID", Desc: "Detached only: tracing identifier recorded on the run; never affects deduplication", Group: "System"},
	{Name: "sw-consumer-idle", Argument: "DUR", Desc: "Detached only, and only if this starts a consumer: how long it stays alive with no work (default 5m)", Group: "System"},
	{Name: "sw-consumer-claim-lease", Argument: "DUR", Desc: "Detached only, and only if this starts a consumer: the lease it stamps on each claimed run, renewed while the run executes (default 3m)", Group: "System"},
	{Name: "sw-output", Argument: "FORMAT", Desc: "Detached only: run-handle format, pretty|json|plain (default: pretty on a TTY, json when piped)", Group: "System"},
	{Name: "sw-verbose", Short: "v", Desc: "Enable debug logging and the complete live JSON event stream", Group: "System"},
	{Name: "sw-start-at", Argument: "STEP", Desc: "Start the run at STEP", Group: "System", Hot: true},
	{Name: "sw-stop-at", Argument: "STEP", Desc: "Stop the run after STEP", Group: "System", Hot: true},
	{Name: "sw-only", Argument: "GLOB", Desc: "Run only jobs whose ID matches GLOB (plus their Needs ancestors)", Group: "System", Hot: true},
	{Name: "sw-no-cache", Desc: "Ignore cached per-node results (writes still happen)", Group: "System", Hot: true},
	{Name: "sw-priority", Argument: "VALUE", Desc: "Local admission priority: an integer, or front/back for one step past the queue as it stands; overrides the plan's own Priority", Group: "System", Hot: true},
	{Name: "sw-no-update", Desc: "Compile against the committed sparks overlay instead of resolving sparks first; CI sets it so the runner needs no resolve", Group: "System"},
	{Name: "sw-no-bincache", Desc: "Build a temporary pipeline binary with debug symbols instead of using the binary cache", Group: "System"},
	{Name: "sw-hash-all-files", Desc: "Hash files Git ignores into the binary cache key, for a build that embeds them", Group: "System"},
	{Name: "sw-allow-unadmitted", Desc: "Run without the admission daemon even when the pipeline reserves host capacity", Group: "System"},
	{Name: "sw-dry-run", Desc: "Run each step's dry-run probe instead of its real action", Group: "System", Hot: true},
	{Name: "sw-allow", Argument: "LABEL[,LABEL...]", Desc: "Authorize risk-labeled steps (repeatable)", Group: "System"},
	{Name: "sw-index", Argument: "PATH", Desc: "Judge the git index at PATH instead of the repository's own (prints an index_bound event naming it)", Group: "System"},
	{Name: "sw-run-handle-file", Argument: "PATH", Desc: "Atomically publish the accepted run's machine-readable handle to PATH", Group: "System"},
	{Name: "target", Argument: "TARGET", Desc: "Run against the named pipeline deployment target (e.g. dev, prod)", Group: "System", Hot: true},
}

// SparkwingFlagDocs returns the canonical sparkwing-owned flag
// documentation. The returned slice is a copy; callers may mutate
// freely.
func SparkwingFlagDocs() []SparkwingFlagDoc {
	out := make([]SparkwingFlagDoc, len(sparkwingFlagDocs))
	copy(out, sparkwingFlagDocs)
	return out
}
