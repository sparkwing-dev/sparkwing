package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	defaultSkipBudget    = 30 * time.Second
	memoKeyBudget        = 30 * time.Second
	afterRunTimeoutLimit = 2 * time.Minute
)

// SkipEvaluation reports the guard decision and zero-based predicates that
// exhausted their budget. Hosts log TimedOut as don't-skip diagnostics.
type SkipEvaluation struct {
	Skip     bool
	TimedOut []int
}

// SkipIf checks owner predicates before approval or dispatch. Each predicate
// gets a reconstructed process, so an expired eval cannot overlap the next one.
// A predicate timeout means don't skip; any other failure prevents dispatch.
func (h *OwnerEvaluator) SkipIf(ctx context.Context, input EvaluationInput) (SkipEvaluation, error) {
	spec, job, err := h.pendingPolicy(ctx, input)
	if err != nil {
		return SkipEvaluation{}, h.policyError(ctx, err)
	}
	budget := defaultSkipBudget
	if job.SkipIfBudgetMS != nil {
		if *job.SkipIfBudgetMS <= 0 || *job.SkipIfBudgetMS > math.MaxInt64/int64(time.Millisecond) {
			return SkipEvaluation{}, executorError(apierr.PlanInvalid, "skip predicate budget must be a positive duration")
		}
		budget = time.Duration(*job.SkipIfBudgetMS) * time.Millisecond
	}
	result := SkipEvaluation{}
	for i, id := range job.Closures.SkipIf {
		err = h.withProcess(ctx, input, spec, func(session *executorSession, code NodeInput) error {
			bounded, cancel, err := h.deps.ChildContext(ctx, budget, 0)
			if err != nil {
				return err
			}
			defer cancel()
			value, evalErr := h.eval(bounded, session, code, id, "skip_if", nil, nil)
			if ctx.Err() == nil && errors.Is(context.Cause(bounded), context.DeadlineExceeded) {
				result.TimedOut = append(result.TimedOut, i)
				return nil
			}
			if evalErr != nil {
				return evalErr
			}
			value = bytes.TrimSpace(value)
			if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
				return executorError(apierr.ProtocolError, "skip predicate must return a boolean")
			}
			result.Skip = bytes.Equal(value, []byte("true"))
			return nil
		})
		if err := h.policyError(ctx, err); err != nil {
			return SkipEvaluation{}, err
		}
		if result.Skip {
			break
		}
	}
	return result, nil
}

func (h *OwnerEvaluator) pendingPolicy(ctx context.Context, input EvaluationInput) (runspec.Spec, nodeproto.JobSpec, error) {
	if err := context.Cause(ctx); err != nil {
		return runspec.Spec{}, nodeproto.JobSpec{}, err
	}
	spec, row, job, err := h.accepted(ctx, input)
	if cause := context.Cause(ctx); cause != nil {
		return spec, job, errors.Join(cause, err)
	}
	if err == nil && (row.Status != store.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" || row.Outcome != "") {
		err = executorError(apierr.IllegalTransition, "owner policy requires an unreleased job")
	}
	return spec, job, err
}

// MemoKey reconstructs the declared key without creating an attempt. Empty
// means the author returned null or declared no memo policy. A key still needs
// server-side scope and read-edge authorization before it can reuse a result.
// NoMemo bypasses reads, so it does not suppress the key needed for a later write.
func (h *OwnerEvaluator) MemoKey(ctx context.Context, input EvaluationInput) (string, error) {
	spec, job, err := h.pendingPolicy(ctx, input)
	if err != nil {
		return "", h.policyError(ctx, err)
	}
	if job.Memo == nil && job.Closures.MemoKey == "" {
		return "", nil
	}
	if job.Memo == nil || job.Closures.MemoKey == "" {
		return "", executorError(apierr.PlanInvalid, "memo policy and key closure must be declared together")
	}
	key := ""
	err = h.withProcess(ctx, input, spec, func(session *executorSession, code NodeInput) error {
		bounded, cancel, err := h.deps.ChildContext(ctx, memoKeyBudget, 0)
		if err != nil {
			return err
		}
		defer cancel()
		value, err := h.eval(bounded, session, code, job.Closures.MemoKey, "memo_key", nil, nil)
		if err != nil {
			return err
		}
		if cause := context.Cause(bounded); cause != nil {
			return cause
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(value, &key); err != nil || key == "" {
			return executorError(apierr.ProtocolError, "memo key must be a nonempty string or null")
		}
		return nil
	})
	if err := h.policyError(ctx, err); err != nil {
		return "", err
	}
	return key, nil
}

func (h *OwnerEvaluator) policyError(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if err == nil {
		return maskHostError(cause, h.deps.Mask)
	}
	code := apierr.SDKError
	var structured *apierr.Error
	if errors.As(err, &structured) {
		code = structured.Code
	}
	return maskHostError(errors.Join(cause, &apierr.Error{Code: code, Message: boundedNodeErrText(h.deps.Mask(err.Error()))}), h.deps.Mask)
}

func (h *OwnerEvaluator) skipLayer(ctx context.Context, input EvaluationInput, row store.NodeRow, log func(context.Context, string, string, nodeproto.FrameLog) error) (OwnerLayerResult, error) {
	result, err := h.SkipIf(ctx, input)
	if err != nil {
		return OwnerLayerResult{}, err
	}
	for _, index := range result.TimedOut {
		err := log(ctx, input.Accepted.RunID, input.NodeID, nodeproto.FrameLog{
			Level: "warn", Message: "SkipIf predicate exceeded its budget; continuing without skipping",
			Attrs: json.RawMessage(fmt.Sprintf(`{"predicate":%d}`, index)),
		})
		if err != nil {
			return OwnerLayerResult{}, err
		}
	}
	if !result.Skip {
		return OwnerLayerResult{}, nil
	}
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil {
		return OwnerLayerResult{}, err
	}
	if job.Approval != nil {
		return OwnerLayerResult{Transition: &store.NodeTransition{
			To: store.NodeSuccess, Cause: store.CauseOwnerPolicy,
			Extra: store.TransitionExtra{
				OwnerPolicy: &store.OwnerPolicyResult{Kind: store.PolicyApprovalBypass, Generation: row.Generation},
				Result:      &store.NodeResult{Outcome: store.NodeSuccess, Reason: "skip_if"},
			},
		}}, nil
	}
	return OwnerLayerResult{Transition: &store.NodeTransition{
		To: store.NodeSkipped, Cause: store.CauseDependency,
		Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeSkipped, Reason: "skip_if"}},
	}}, nil
}
