package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const defaultSkipBudget = 30 * time.Second

// safety: bounds the after_run eval process of a timed-out attempt, whose own budget is spent.
const afterRunTimeoutLimit = 2 * time.Minute

// safety: the skip_if closures run in an eval process of their own before the
// node starts, so a skipped node never gets an attempt, and a closure past its
// budget dies with that process instead of running beside the body. A custom
// executor owns the whole node, so without Attempts the layer is off.
func skipIfLayer(cfg *Config, next Executor) Executor {
	if cfg.Attempts == nil {
		return next
	}
	return func(ctx context.Context, n *NodeRun) Result {
		ids := closuresOf(n).SkipIf
		if len(ids) == 0 {
			return next(ctx, n)
		}
		budget := defaultSkipBudget
		if m := n.Plan.Modifiers; m != nil && m.SkipIfBudgetMS > 0 {
			budget = time.Duration(m.SkipIfBudgetMS) * time.Millisecond
		}
		reason := ""
		res := cfg.Attempts.process(ctx, cfg.Session, n, func(ctx context.Context, sess *Session, log Sink) Result {
			if err := nodeReplan(ctx, sess, n); err != nil {
				return failed(err)
			}
			for i, id := range ids {
				msg := fmt.Sprintf("evaluating SkipIf predicate (budget %s)", budget)
				if len(ids) > 1 {
					msg = fmt.Sprintf("evaluating SkipIf predicate %d/%d (budget %s)", i+1, len(ids), budget)
				}
				closureNote(log, LevelInfo, msg)
				skip, err := skipIfEval(ctx, sess, log, id, i, budget)
				if err != nil {
					return failed(err)
				}
				if skip {
					reason = fmt.Sprintf("SkipIf predicate %d returned true", i)
					break
				}
			}
			return Result{Outcome: sparkwing.Success}
		})
		switch {
		case res.Outcome != sparkwing.Success:
			return res
		case reason != "":
			return Result{Outcome: sparkwing.Skipped, Err: errors.New(reason)}
		}
		return next(ctx, n)
	}
}

// safety: a closure that errors or overruns counts as don't-skip, as in the
// in-process dispatcher; only a failed reply or a broken session fails the node.
func skipIfEval(ctx context.Context, sess *Session, log Sink, id string, i int, budget time.Duration) (bool, error) {
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var er nodeproto.EvalResult
	err := ask(bctx, sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: id}, &er)
	switch {
	case err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
		closureNote(log, "error", fmt.Sprintf("SkipIf predicate %d exceeded %s budget (defaulting to don't-skip); raise via SkipIf(fn, sparkwing.SkipBudget(d))", i, budget))
		return false, nil
	case err != nil:
		return false, err
	case er.Error != nil:
		closureNote(log, "error", fmt.Sprintf("SkipIf predicate %d failed: %s (defaulting to don't-skip)", i, *er.Error))
		return false, nil
	}
	var skip bool
	if err := json.Unmarshal(er.Value, &skip); err != nil {
		return false, hostError(nodeproto.CodeProtocolError, "skip_if %s returned %s, not a boolean", id, er.Value)
	}
	return skip, nil
}

// safety: the protocol's attempt order: before_run on attempt 1 only, verify after a successful body,
// and after_run only once no attempt follows, so each sees the state today's dispatcher gives it.
func withClosures(ctx context.Context, sess *Session, log Sink, n *NodeRun, body func() Result) Result {
	c := closuresOf(n)
	if max(n.Attempt, 1) == 1 {
		for i, id := range c.BeforeRun {
			msg, err := closureEval(ctx, sess, id, nil)
			if err == nil && msg == nil {
				continue
			}
			if err == nil {
				err = fmt.Errorf("BeforeRun hook %d: %s", i, *msg)
				closureNote(log, "error", err.Error())
			}
			res := failed(err)
			res.Final = true
			return res
		}
	}
	res := body()
	if res.Outcome == sparkwing.Success && c.Verify != "" {
		msg, err := closureEval(ctx, sess, c.Verify, nil)
		switch {
		case err != nil:
			res = failed(err)
		case msg != nil:
			verr := &sparkwing.VerifyError{Err: errors.New(*msg)}
			closureNote(log, "error", verr.Error())
			res = Result{Outcome: sparkwing.Failed, Err: verr, Reason: store.FailureVerify}
		}
	}
	// safety: an attempt stopped by its timeout or the run's cancel has a dying process;
	// the timeout layer runs after_run in an eval process instead.
	if ctx.Err() != nil || !afterRunDue(n, res) {
		return res
	}
	return afterRunEval(ctx, sess, log, c.AfterRun, res)
}

func afterRunDue(n *NodeRun, res Result) bool {
	return len(closuresOf(n).AfterRun) > 0 && (res.Final || n.Retried == nil || !n.Retried(res))
}

// safety: a failed reply fails only a node that succeeded; a failed node keeps
// its own error, which is the one worth reading.
func afterRunEval(ctx context.Context, sess *Session, log Sink, ids []string, res Result) Result {
	input, err := json.Marshal(afterRunArgOf(res))
	if err != nil {
		return failed(err)
	}
	for i, id := range ids {
		msg, err := closureEval(ctx, sess, id, input)
		switch {
		case err != nil && res.Outcome == sparkwing.Success:
			res = failed(fmt.Errorf("AfterRun hook %d: %w", i, err))
		case err != nil:
			closureNote(log, "error", fmt.Sprintf("AfterRun hook %d: %v", i, err))
		case msg != nil:
			closureNote(log, "error", fmt.Sprintf("AfterRun hook %d: %s", i, *msg))
		}
	}
	return res
}

// safety: a timed-out attempt's process is gone, so after_run gets a fresh one, as today's executor still ran it.
func afterRunAfterTimeout(ctx context.Context, cfg *Config, n *NodeRun, res Result) {
	if cfg == nil || cfg.Attempts == nil || ctx.Err() != nil || !afterRunDue(n, res) {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, afterRunTimeoutLimit)
	defer cancel()
	cfg.Attempts.process(lctx, cfg.Session, n, func(ctx context.Context, sess *Session, log Sink) Result {
		if err := nodeReplan(ctx, sess, n); err != nil {
			closureNote(log, "error", fmt.Sprintf("AfterRun hooks after the timeout: %v", err))
			return failed(err)
		}
		return afterRunEval(ctx, sess, log, closuresOf(n).AfterRun, res)
	})
}

type afterRunArg struct {
	Outcome nodeproto.Outcome `json:"outcome"`
	Error   string            `json:"error,omitempty"`
}

func afterRunArgOf(res Result) afterRunArg {
	if res.Outcome == sparkwing.Success {
		return afterRunArg{Outcome: nodeproto.OutcomeSuccess}
	}
	return afterRunArg{Outcome: nodeproto.OutcomeFailed, Error: errText(resultErr(res))}
}

// safety: msg is the closure's own error and err a failed reply or broken session, which callers treat differently.
func closureEval(ctx context.Context, sess *Session, id string, input json.RawMessage) (msg *string, err error) {
	var er nodeproto.EvalResult
	if err := ask(ctx, sess, nodeproto.Request{Op: nodeproto.OpEval, Closure: id, Input: input}, &er); err != nil {
		return nil, err
	}
	return er.Error, nil
}

func closuresOf(n *NodeRun) nodeproto.PlanClosures {
	if c := n.Plan.Closures; c != nil {
		return *c
	}
	return nodeproto.PlanClosures{}
}

func closureNote(log Sink, level, msg string) {
	log.Record(nodeproto.LogRecord{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: level, Msg: msg})
}
