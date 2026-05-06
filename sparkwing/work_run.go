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
// MarkDone so downstream TypedStep[T].Get(ctx) calls resolve. The
// Work's designated ResultStep (set via SetResult) becomes the
// returned output that the orchestrator stores as the Node's typed
// output.
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

	// IMP-007: resolve --start-at / --stop-at against this Work's
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
		firstErr error
		running  int
	)
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		mu.Unlock()
	}
	getErr := func() error {
		mu.Lock()
		defer mu.Unlock()
		return firstErr
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
			setErr(res.err)
			continue
		}
		for _, c := range children[res.id] {
			indeg[c]--
		}
		if getErr() == nil {
			schedule()
		}
	}

	if err := getErr(); err != nil {
		return nil, err
	}
	if rs := w.ResultStep(); rs != nil {
		return rs.Output(), nil
	}
	return nil, nil
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
	// outside the selected range (IMP-007).
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

	// IMP-007: range skip wins over user-authored SkipIf predicates so
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

	// IMP-014: under --dry-run, a step that declared neither a
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

	out, err := dispatchItem(ctx, it, parentNodeID, handler)
	elapsed := time.Since(start)

	if err != nil {
		if !it.isHidden {
			emitStepEvent(ctx, it.id, "step_end", elapsed, err)
		}
		done <- stepResult{id: it.id, err: fmt.Errorf("step %q: %w", it.id, err)}
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
		// IMP-014: dispatch under --dry-run prefers DryRunFn over
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
		return nil, fmt.Errorf("sparkwing: SpawnNodeForEach: items must be a slice, got %T", spec.items)
	}
	fnv := reflect.ValueOf(spec.fn)
	if fnv.Kind() != reflect.Func {
		return nil, fmt.Errorf("sparkwing: SpawnNodeForEach: fn must be a func, got %T", spec.fn)
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
			return nil, fmt.Errorf("sparkwing: SpawnNodeForEach: fn must return (string, sparkwing.Workable), got %d return values", len(out))
		}
		idStr, _ := out[0].Interface().(string)
		job, ok := out[1].Interface().(Workable)
		if !ok || idStr == "" {
			return nil, fmt.Errorf("sparkwing: SpawnNodeForEach: fn must return (string, sparkwing.Workable), got (%T, %T) for item %d", out[0].Interface(), out[1].Interface(), i)
		}
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
