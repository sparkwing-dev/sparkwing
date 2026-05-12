package sparkwing

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// RunWork executes w's step + spawn DAG. Items run in dependency
// order (declared via Needs); independent items run in parallel.
//
// For each successful WorkStep the typed output is recorded via
// MarkDone so downstream sparkwing.StepGet[T](ctx, step) calls resolve.
// RunWork itself returns (nil, err); the Node's typed output is
// recorded on the *WorkStep the Job's Work returned and read back by
// the orchestrator via Node.ResultStep().Output().
//
// SpawnNode declarations dispatch through the SpawnHandler installed
// in ctx; the spawning runner remains alive across the child's
// lifecycle. SpawnNodeForEach generators dispatch every (id, job)
// pair in parallel, fail-fast.
//
// Emits step_start / step_end / step_skipped LogRecord events for
// each executed step or spawn (Attrs: id, kind, duration_ms,
// outcome, optional error).
//
// Fail-fast: the first item error fails the run; the shared ctx is
// cancelled so in-flight siblings unwind. Skipped steps (any SkipIf
// returns true) propagate to downstream as if they succeeded with
// no output.
func RunWork(ctx context.Context, w *Work) (any, error) {
	if w == nil {
		return nil, nil
	}
	steps := w.Steps()
	spawns := w.Spawns()
	gens := w.SpawnGens()

	if len(steps) == 0 && len(spawns) == 0 && len(gens) == 0 {
		return nil, nil
	}

	parentNodeID := NodeFromContext(ctx)
	handler := SpawnHandlerFromContext(ctx)
	if (len(spawns) > 0 || len(gens) > 0) && handler == nil {
		return nil, fmt.Errorf("sparkwing: RunWork: Work declares %d Spawn(s) but no SpawnHandler is installed in ctx; spawn dispatch requires the orchestrator-provided handler", len(spawns)+len(gens))
	}

	// Steps, single SpawnNodes, and SpawnNodeForEach generators all
	// schedule through the same dep-graph; the kind tag selects the
	// executor.
	items := make(map[string]*workItem, len(steps)+len(spawns)+len(gens))
	addItem := func(it *workItem) {
		if _, exists := items[it.id]; exists {
			panic(fmt.Sprintf("sparkwing: RunWork: duplicate item id %q across steps/spawns", it.id))
		}
		items[it.id] = it
	}
	for _, s := range steps {
		addItem(&workItem{
			id:     s.ID(),
			kind:   itemStep,
			deps:   s.DepIDs(),
			step:   s,
			skipIf: s.SkipPredicates(),
		})
	}
	for _, sp := range spawns {
		addItem(&workItem{
			id:     sp.ID(),
			kind:   itemSpawn,
			deps:   sp.DepIDs(),
			spawn:  sp,
			skipIf: sp.SkipPredicates(),
		})
	}
	for _, g := range gens {
		addItem(&workItem{
			id:       g.syntheticID(),
			kind:     itemSpawnEach,
			deps:     g.DepIDs(),
			gen:      g,
			isHidden: true,
		})
	}

	indeg := make(map[string]int, len(items))
	children := make(map[string][]string, len(items))
	for id, it := range items {
		if _, ok := indeg[id]; !ok {
			indeg[id] = 0
		}
		for _, d := range it.deps {
			if _, ok := items[d]; !ok {
				return nil, fmt.Errorf("sparkwing: RunWork: item %q depends on unknown item %q", id, d)
			}
			indeg[id]++
			children[d] = append(children[d], id)
		}
	}

	// Resolve --start-at / --stop-at against this Work's
	// items. The orchestrator already validated the strings exist
	// somewhere in the Plan; here we apply the range only if at least
	// one bound matches a local item, so the same range plumbed
	// through ctx for a multi-Job pipeline is a no-op for Works
	// that don't contain the named step.
	if r, ok := stepRangeFromContext(ctx); ok && (r.start != "" || r.stop != "") {
		_, hasStart := items[r.start]
		_, hasStop := items[r.stop]
		if r.start == "" || hasStart || r.stop == "" || hasStop {
			if (r.start != "" && hasStart) || (r.stop != "" && hasStop) {
				skips := computeStepRangeSkips(items, children, r)
				for id, reason := range skips {
					if it, ok := items[id]; ok {
						it.rangeSkipReason = reason
					}
				}
			}
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan stepResult, len(items))
	pending := make(map[string]bool, len(items))
	for id := range items {
		pending[id] = true
	}

	var (
		mu       sync.Mutex
		firstErr error // returned by RunWork; Optional steps don't fill this
		fatalErr error // halts further scheduling; non-ContinueOnError steps fill this
		running  int
	)
	// setErr records a failure and decides whether to cancel siblings.
	// item is the failed workItem so we can read its WorkStep flags;
	// nil item (spawn/spawnEach failure) follows the legacy fail-fast
	// path -- only WorkSteps carry the Optional / ContinueOnError
	// opt-outs.
	setErr := func(it *workItem, err error) {
		mu.Lock()
		defer mu.Unlock()
		step := stepOf(it)
		// Optional masks the failure from the rollup. ContinueOnError
		// (implied by Optional, also set on its own) skips the cancel
		// + skips the schedule-halt so siblings keep running.
		if step == nil || !step.IsOptional() {
			if firstErr == nil {
				firstErr = err
			}
		}
		if step == nil || !step.IsContinueOnError() {
			if fatalErr == nil {
				fatalErr = err
			}
			cancel()
		}
	}
	getErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return firstErr
	}
	getFatalErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return fatalErr
	}

	schedule := func() {
		ready := make([]*workItem, 0, len(pending))
		for id := range pending {
			if indeg[id] == 0 {
				ready = append(ready, items[id])
			}
		}
		for _, it := range ready {
			delete(pending, it.id)
			running++
			go runOneItem(runCtx, it, parentNodeID, handler, done)
		}
	}

	schedule()
	if running == 0 {
		return nil, fmt.Errorf("sparkwing: RunWork: cycle detected -- no item has zero in-degree (items=%d)", len(items))
	}

	for running > 0 {
		res := <-done
		running--

		if res.err != nil {
			it := items[res.id]
			setErr(it, res.err)
			// ContinueOnError lets dependents that .Needs() this step
			// still dispatch -- decrement the in-degree like a normal
			// completion. Default behavior leaves indeg untouched so
			// dependents stay blocked (cascade-skip).
			step := stepOf(it)
			if step != nil && step.IsContinueOnError() {
				for _, c := range children[res.id] {
					indeg[c]--
				}
			}
			if getFatalErr() == nil {
				schedule()
			}
			continue
		}
		for _, c := range children[res.id] {
			indeg[c]--
		}
		if getFatalErr() == nil {
			schedule()
		}
	}

	if err := getErr(); err != nil {
		return nil, err
	}
	return nil, nil
}

// stepOf returns the WorkStep for a step-kind item, or nil for
// spawn / spawnEach items (which don't carry Optional /
// ContinueOnError flags today).
func stepOf(it *workItem) *WorkStep {
	if it == nil || it.kind != itemStep {
		return nil
	}
	return it.step
}

type itemKind int

const (
	itemStep itemKind = iota
	itemSpawn
	itemSpawnEach
)

// workItem unifies steps and spawns under one scheduling entity.
type workItem struct {
	id       string
	kind     itemKind
	deps     []string
	skipIf   []SkipPredicate
	step     *WorkStep
	spawn    *SpawnSpec
	gen      *SpawnGenSpec
	isHidden bool // true for synthetic SpawnNodeForEach items
	// rangeSkipReason, when non-empty, short-circuits the item with a
	// `step_skipped` event whose Attrs.reason carries this string.
	// Populated by RunWork when --start-at / --stop-at puts the item
	// outside the selected range.
	rangeSkipReason string
}

// runOneItem executes a single item to terminal: range skip, SkipIf
// evaluation, then dispatch to the kind-specific executor. Panics
// in user code are surfaced as item errors so the runner doesn't
// crash.
func runOneItem(ctx context.Context, it *workItem, parentNodeID string, handler SpawnHandler, done chan<- stepResult) {
	defer func() {
		if r := recover(); r != nil {
			done <- stepResult{id: it.id, err: fmt.Errorf("item %q panicked: %v", it.id, r)}
		}
	}()

	// Range skip wins over user-authored SkipIf predicates so
	// `step_skipped` events carry the operator's intent ("outside
	// --start-at..--stop-at range") rather than whatever predicate
	// happened to match. Hidden synthetic items (SpawnNodeForEach
	// fan-out) never emit step_skipped to keep the dashboard clean.
	if it.rangeSkipReason != "" {
		if !it.isHidden {
			emitStepSkippedWithReason(ctx, it.id, it.rangeSkipReason)
		}
		it.markDone(nil)
		done <- stepResult{id: it.id}
		return
	}

	for _, p := range it.skipIf {
		if p(ctx) {
			if !it.isHidden {
				emitStepEvent(ctx, it.id, "step_skipped", 0, nil)
			}
			it.markDone(nil)
			done <- stepResult{id: it.id}
			return
		}
	}

	// Under --dry-run, a step that declared neither a
	// dry-run body nor an explicit SafeWithoutDryRun marker is
	// soft-skipped with reason `no_dry_run_defined`. The dispatch
	// path below selects DryRunFn vs Fn for the cases that DO have
	// a defined dispatch; this branch handles only the "missing
	// contract" case so the run logs make the gap visible.
	if it.kind == itemStep && IsDryRun(ctx) && it.step.dryRunFn == nil && !it.step.safeWithoutDryRun {
		if !it.isHidden {
			emitStepSkippedWithReason(ctx, it.id, "no_dry_run_defined")
		}
		it.markDone(nil)
		done <- stepResult{id: it.id}
		return
	}

	start := time.Now()
	if !it.isHidden {
		emitStepEvent(ctx, it.id, "step_start", 0, nil)
	}

	// Push the step ID onto ctx for the body's duration so log records
	// emitted *inside* the step (Info, Exec lines, etc.) carry it in
	// their breadcrumb. step_start above and step_end below intentionally
	// fire on the un-stepped ctx so the start/end lines render at the
	// node level without duplicating the step name in their breadcrumb.
	stepCtx := WithStep(ctx, it.id)
	out, err := dispatchItem(stepCtx, it, parentNodeID, handler)
	elapsed := time.Since(start)

	if err != nil {
		if !it.isHidden {
			emitStepEvent(ctx, it.id, "step_end", elapsed, err)
		}
		done <- stepResult{id: it.id, err: &StepError{StepID: it.id, Cause: err}}
		return
	}
	it.markDone(out)
	if !it.isHidden {
		emitStepEvent(ctx, it.id, "step_end", elapsed, nil)
	}
	done <- stepResult{id: it.id, out: out}
}

func dispatchItem(ctx context.Context, it *workItem, parentNodeID string, handler SpawnHandler) (any, error) {
	switch it.kind {
	case itemStep:
		// Dispatch under --dry-run prefers DryRunFn over
		// the apply Fn. The "step has neither" case is handled
		// upstream in runOneItem before step_start fires; by the
		// time we reach dispatchItem the step is guaranteed to
		// have a runnable body for the current mode.
		if IsDryRun(ctx) && it.step.dryRunFn != nil {
			return nil, it.step.dryRunFn(ctx)
		}
		return it.step.Fn()(ctx)
	case itemSpawn:
		return runOneSpawn(ctx, it.spawn, parentNodeID, handler)
	case itemSpawnEach:
		return runSpawnEach(ctx, it.gen, parentNodeID, handler)
	default:
		return nil, fmt.Errorf("sparkwing: RunWork: unknown item kind %v for %q", it.kind, it.id)
	}
}

func (it *workItem) markDone(out any) {
	switch it.kind {
	case itemStep:
		it.step.MarkDone(out)
	case itemSpawn:
		it.spawn.MarkDone(out)
	}
}

func runOneSpawn(ctx context.Context, spec *SpawnSpec, parentNodeID string, handler SpawnHandler) (any, error) {
	out, err := handler.Spawn(ctx, parentNodeID, spec.id, spec.job)
	return out, err
}

// runSpawnEach iterates the generator's slice and dispatches every
// (id, job) pair through the handler in parallel. Fail-fast.
func runSpawnEach(ctx context.Context, spec *SpawnGenSpec, parentNodeID string, handler SpawnHandler) (any, error) {
	rv := reflect.ValueOf(spec.items)
	if rv.Kind() != reflect.Slice {
		return nil, fmt.Errorf("sparkwing: JobSpawnEach: items must be a slice, got %T", spec.items)
	}
	fnv := reflect.ValueOf(spec.fn)
	if fnv.Kind() != reflect.Func {
		return nil, fmt.Errorf("sparkwing: JobSpawnEach: fn must be a func, got %T", spec.fn)
	}
	n := rv.Len()
	if n == 0 {
		return nil, nil
	}

	type childResult struct {
		idx int
		err error
	}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan childResult, n)

	for i := range n {
		elem := rv.Index(i).Interface()
		out := fnv.Call([]reflect.Value{reflect.ValueOf(elem)})
		if len(out) != 2 {
			return nil, fmt.Errorf("sparkwing: JobSpawnEach: fn must return (string, sparkwing.Workable) or (string, func(ctx) error), got %d return values", len(out))
		}
		idStr, _ := out[0].Interface().(string)
		if idStr == "" {
			return nil, fmt.Errorf("sparkwing: JobSpawnEach: fn returned empty id for item %d", i)
		}
		job := coerceSpawnEachJob(out[1].Interface())
		go func() {
			_, err := handler.Spawn(childCtx, parentNodeID, idStr, job)
			results <- childResult{idx: i, err: err}
		}()
	}

	var firstErr error
	for range n {
		r := <-results
		if r.err != nil && firstErr == nil {
			firstErr = r.err
			cancel()
		}
	}
	return nil, firstErr
}

type stepResult struct {
	id  string
	out any
	err error
}

// StepError wraps a step body's error with the originating step ID.
// Loggers can use errors.As to pull the StepID off and tag the log
// record with the step in its breadcrumb -- otherwise the runner-side
// inline error log would render at the bare node level even though
// the surrounding step output is in the same step's context.
type StepError struct {
	StepID string
	Cause  error
}

func (e *StepError) Error() string {
	if e == nil || e.Cause == nil {
		return ""
	}
	return fmt.Sprintf("step %q: %s", e.StepID, e.Cause.Error())
}

func (e *StepError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func emitStepEvent(ctx context.Context, stepID, event string, elapsed time.Duration, err error) {
	attrs := map[string]any{"step": stepID}
	if elapsed > 0 {
		attrs["duration_ms"] = elapsed.Milliseconds()
	}
	level := "info"
	if err != nil {
		attrs["error"] = err.Error()
		attrs["outcome"] = "failed"
		level = "error"
	} else if event == "step_end" {
		attrs["outcome"] = "success"
	} else if event == "step_skipped" {
		attrs["outcome"] = "skipped"
	}
	LoggerFromContext(ctx).Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: level,
		Node:  NodeFromContext(ctx),
		Event: event,
		Msg:   stepID,
		Attrs: attrs,
	}))
}
