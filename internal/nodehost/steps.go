package nodehost

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: a step graph the host cannot order would hang an attempt or run a
// step before its need, so the run fails before any node starts.
func validateSteps(n nodeproto.JobSpec) error {
	if len(n.Steps) == 0 {
		return nil
	}
	indeg := make(map[string]int, len(n.Steps))
	children := map[string][]string{}
	for _, st := range n.Steps {
		if st.ID == "" {
			return invalid("node %s holds a step with no id", n.ID)
		}
		if _, dup := indeg[st.ID]; dup {
			return invalid("node %s holds step %s twice", n.ID, st.ID)
		}
		indeg[st.ID] = len(st.Needs)
	}
	var ready []string
	for _, st := range n.Steps {
		for _, d := range st.Needs {
			if _, ok := indeg[d]; !ok {
				return invalid("step %s of node %s needs %s, which the node does not hold", st.ID, n.ID, d)
			}
			children[d] = append(children[d], st.ID)
		}
		if len(st.Needs) == 0 {
			ready = append(ready, st.ID)
		}
	}
	for done := 0; len(ready) > 0; done++ {
		id := ready[0]
		ready = ready[1:]
		for _, c := range children[id] {
			if indeg[c]--; indeg[c] == 0 {
				ready = append(ready, c)
			}
		}
		if done+1 == len(n.Steps) {
			return nil
		}
	}
	return invalid("node %s has a step dependency cycle", n.ID)
}

// safety: ports the SDK's in-process RunWork decision for decision, so a
// pipeline's steps end the same whichever executor drives them.
type stepRun struct {
	sess    *Session
	sink    *LogSink
	n       *NodeRun
	attempt int
	skips   map[string]string
}

type stepDone struct {
	id      string
	started bool
	elapsed time.Duration
	output  json.RawMessage
	err     error
}

func driveSteps(ctx context.Context, sess *Session, sink *LogSink, n *NodeRun, attempt int) Result {
	w := n.Plan
	r := &stepRun{sess: sess, sink: sink, n: n, attempt: attempt}
	steps := make(map[string]nodeproto.StepSpec, len(w.Steps))
	indeg := make(map[string]int, len(w.Steps))
	children := map[string][]string{}
	pending := map[string]bool{}
	for _, st := range w.Steps {
		steps[st.ID], indeg[st.ID], pending[st.ID] = st, len(st.Needs), true
		for _, d := range st.Needs {
			children[d] = append(children[d], st.ID)
		}
	}
	r.skips = rangeSkips(w.Steps, children, n.Run.StartAt, n.Run.StopAt)
	collect := w.FailurePolicy == string(sparkwing.CollectAll)

	var (
		firstErr, fatal    error
		fatalStep          string
		failFastAt, lastAt time.Time
		cancelledSteps     []string
		output             json.RawMessage
		cancels            sync.WaitGroup
	)
	running := map[string]bool{}
	finished := map[string]bool{}
	cancelSent := map[string]bool{}
	done := make(chan stepDone)

	cancelPending := func() {
		for changed := true; changed; {
			changed = false
			for _, st := range w.Steps {
				if !pending[st.ID] || st.Finally || indeg[st.ID] != 0 {
					continue
				}
				delete(pending, st.ID)
				finished[st.ID], changed = true, true
				cancelledSteps, lastAt = append(cancelledSteps, st.ID), time.Now()
				r.end(st.ID, 0, nil, true)
				for _, c := range children[st.ID] {
					indeg[c]--
				}
			}
		}
	}
	// safety: the SDK cancels only the step's context, so the host keeps
	// waiting for the step's reply, as RunWork waits for a cancelled body.
	cancelSiblings := func() {
		for id := range running {
			if steps[id].Finally || cancelSent[id] {
				continue
			}
			cancelSent[id] = true
			cancels.Add(1)
			go func() {
				defer cancels.Done()
				// safety: a cancel that fails leaves the step to finish on its
				// own, and a broken session fails the step's own reply too.
				if _, err := sess.Call(ctx, nodeproto.Request{Op: nodeproto.OpCancel, Job: n.Plan.ID, Step: id}); err != nil {
					n.Run.Logger.Warn("nodehost: cancel step", "run", n.Run.ID, "node", n.Plan.ID, "step", id, "err", err)
				}
			}()
		}
	}
	schedule := func() {
		if fatal != nil || ctx.Err() != nil {
			cancelPending()
		}
		for _, st := range w.Steps {
			if pending[st.ID] && indeg[st.ID] == 0 && (fatal == nil || st.Finally) {
				delete(pending, st.ID)
				running[st.ID] = true
				settled := settledSteps(w.Steps, finished)
				go func() { done <- r.run(ctx, st, settled) }()
			}
		}
	}

	schedule()
	for len(running) > 0 {
		res := <-done
		delete(running, res.id)
		finished[res.id] = true
		st := steps[res.id]
		if res.err == nil {
			if res.id == w.ResultStep {
				output = res.output
			}
			for _, c := range children[res.id] {
				indeg[c]--
			}
			if res.started {
				r.end(res.id, res.elapsed, nil, false)
			}
			schedule()
			continue
		}
		cancelled := cancelSent[res.id] || ctx.Err() != nil
		if res.started {
			r.end(res.id, res.elapsed, res.err, cancelled)
		}
		switch {
		case cancelled && fatal != nil:
			cancelledSteps, lastAt = append(cancelledSteps, res.id), time.Now()
		case cancelled:
			firstErr = cmp.Or(firstErr, ctx.Err())
			cancelPending()
		default:
			if !st.Optional && firstErr == nil {
				firstErr = fmt.Errorf("step %q: %w", res.id, res.err)
			}
			if !collect && !st.ContinueOnError {
				if fatal == nil {
					fatal, fatalStep, failFastAt = res.err, res.id, time.Now()
				}
				cancelSiblings()
			}
			if fatal != nil {
				cancelPending()
			}
		}
		for _, c := range children[res.id] {
			if st.ContinueOnError || steps[c].Finally {
				indeg[c]--
			}
		}
		schedule()
	}
	cancels.Wait()

	if fatalStep != "" {
		slices.Sort(cancelledSteps)
		latency := time.Duration(0)
		if !lastAt.IsZero() {
			latency = lastAt.Sub(failFastAt)
		}
		r.sink.host(sparkwing.LogRecord{Level: "info", Event: "work_fail_fast", Msg: fatalStep, Attrs: map[string]any{
			"trigger_step":            fatalStep,
			"cancelled_siblings":      len(cancelledSteps),
			"cancelled_steps":         cancelledSteps,
			"cancellation_latency_ms": latency.Milliseconds(),
		}})
	}
	if firstErr != nil {
		return failed(firstErr)
	}
	return Result{Outcome: sparkwing.Success, Output: output}
}

func (r *stepRun) run(ctx context.Context, st nodeproto.StepSpec, settled []string) stepDone {
	d := stepDone{id: st.ID}
	if reason := r.skips[st.ID]; reason != "" {
		r.skip(st.ID, reason)
		return d
	}
	skip, err := r.skipIf(ctx, st, settled)
	switch {
	case err != nil:
		d.err = err
		return d
	case skip:
		r.skip(st.ID, "")
		return d
	case r.n.Run.Context.DryRun && !st.HasDryRun && !st.SafeWithoutDryRun:
		r.skip(st.ID, "no_dry_run_defined")
		return d
	}
	d.started = true
	start := time.Now()
	r.record("info", "step_start", st.ID, map[string]any{"step": st.ID})
	var rr nodeproto.RunResult
	err = ask(ctx, r.sess, nodeproto.Request{
		Op: nodeproto.OpRunStep, Job: r.n.Plan.ID, Step: st.ID, Attempt: r.attempt, Settled: settled, Failure: r.n.Failure,
	}, &rr)
	d.elapsed = time.Since(start)
	switch {
	case err != nil:
		d.err = err
	case rr.Outcome == "failed":
		d.err = rr.Error
	default:
		d.output = rr.Output
	}
	return d
}

// safety: a skip_if that throws fails its step, as a panicking predicate
// does in process, so a broken guard never runs what it was meant to skip.
func (r *stepRun) skipIf(ctx context.Context, st nodeproto.StepSpec, settled []string) (bool, error) {
	for _, id := range st.Closures.SkipIf {
		var res nodeproto.EvalResult
		if err := ask(ctx, r.sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: id, Attempt: r.attempt, Settled: settled}, &res); err != nil {
			return false, err
		}
		value, err := evalValue(res, "skip_if")
		if err != nil {
			return false, err
		}
		var skip bool
		if err := json.Unmarshal(value, &skip); err != nil {
			return false, err
		}
		if skip {
			return true, nil
		}
	}
	return false, nil
}

func (r *stepRun) skip(id, reason string) {
	attrs := map[string]any{"step": id, "outcome": "skipped"}
	if reason != "" {
		attrs["reason"] = reason
	}
	r.record("info", "step_skipped", id, attrs)
}

func (r *stepRun) end(id string, elapsed time.Duration, err error, cancelled bool) {
	attrs := map[string]any{"step": id, "duration_ms": elapsed.Milliseconds(), "outcome": string(sparkwing.Success)}
	level := "info"
	switch {
	case cancelled:
		attrs["outcome"] = string(sparkwing.Cancelled)
	case err != nil:
		attrs["outcome"], attrs["error"], level = string(sparkwing.Failed), err.Error(), "error"
	}
	r.record(level, "step_end", id, attrs)
}

// safety: a row the store refuses is logged and never changes the step's
// outcome, as with the in-process node log observer.
func (r *stepRun) record(level, event, id string, attrs map[string]any) {
	st, ctx, run, node := r.n.Run.State, r.sink.ctx, r.n.Run.ID, r.n.Plan.ID
	var err error
	switch event {
	case "step_start":
		err = st.StartNodeStep(ctx, run, node, id)
	case "step_skipped":
		err = st.SkipNodeStep(ctx, run, node, id)
	default:
		status := store.StepPassed
		switch attrs["outcome"] {
		case string(sparkwing.Failed):
			status = store.StepFailed
		case string(sparkwing.Cancelled):
			status = store.StepCancelled
		}
		err = st.FinishNodeStep(ctx, run, node, id, status)
	}
	if err != nil {
		r.sink.host(sparkwing.LogRecord{
			Level: "warn", Event: "state_write_failed", Step: id, Msg: "step state write failed",
			Attrs: map[string]any{"run_id": run, "operation": event, "error": err.Error()},
		})
	}
	r.sink.host(sparkwing.LogRecord{Level: level, Event: event, Msg: id, Attrs: attrs})
}
