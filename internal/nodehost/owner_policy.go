package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// approvalSkipIf evaluates an approval-only guard; executable jobs evaluate under their award.
func (h *OwnerEvaluator) approvalSkipIf(ctx context.Context, input EvaluationInput) (skipEvaluation, error) {
	spec, row, job, err := h.accepted(ctx, input)
	if err == nil && (job.Approval == nil || row.Status != runstate.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" || row.Outcome != "") {
		err = executorError(apierr.IllegalTransition, "owner guard requires an unreleased approval")
	}
	if err != nil {
		return skipEvaluation{}, h.policyError(ctx, err)
	}
	evaluator := predicateEvaluator{code: &h.sdkCode, childContext: h.deps.ChildContext, process: func(ctx context.Context, evaluate func(*executorSession, NodeInput) error) error {
		return h.evaluateProcess(ctx, input, spec, evaluate)
	}}
	result, err := evaluator.skipIf(ctx, job)
	return result, h.policyError(ctx, err)
}

func (h *OwnerEvaluator) policyError(ctx context.Context, err error) error {
	cause := context.Cause(ctx)
	if err == nil {
		return maskHostError(cause, h.deps.Masker)
	}
	code := apierr.SDKError
	var structured *apierr.Error
	if errors.As(err, &structured) {
		code = structured.Code
	}
	return maskHostError(errors.Join(cause, &apierr.Error{Code: code, Message: boundedNodeErrText(h.deps.Masker.Mask(err.Error()))}), h.deps.Masker)
}

func (h *OwnerEvaluator) skipLayer(ctx context.Context, input EvaluationInput, row runstate.NodeRow, log func(context.Context, string, string, nodeproto.FrameLog) error) (ownerLayerResult, error) {
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil {
		return ownerLayerResult{}, err
	}
	if job.Approval == nil {
		return ownerLayerResult{}, nil
	}
	result, err := h.approvalSkipIf(ctx, input)
	if err != nil {
		var refusal *apierr.Error
		if context.Cause(ctx) != nil || store.IsProtocolErr(err) || errors.As(err, &refusal) && (refusal.Code == apierr.Forbidden || refusal.Code == apierr.Unauthenticated || refusal.Code == apierr.LockHeld) {
			return ownerLayerResult{}, err
		}
		return ownerLayerResult{Transition: store.OwnerPolicyNode{Outcome: runstate.NodeFailed, OwnerPolicy: &runstate.OwnerPolicyResult{Kind: runstate.PolicyApprovalFail, Generation: row.Generation}, Result: &runstate.NodeResult{Outcome: runstate.NodeFailed, Reason: string(apierr.SDKError), MaskedError: boundedNodeErrText(h.deps.Masker.Mask(err.Error()))}}}, nil
	}
	for _, index := range result.TimedOut {
		err := log(ctx, input.Accepted.RunID, input.NodeID, nodeproto.FrameLog{
			Level: "warn", Message: "SkipIf predicate exceeded its budget; continuing without skipping",
			Attrs: json.RawMessage(fmt.Sprintf(`{"predicate":%d}`, index)),
		})
		if err != nil {
			return ownerLayerResult{}, err
		}
	}
	if !result.Skip {
		return ownerLayerResult{}, nil
	}
	return ownerLayerResult{Transition: store.OwnerPolicyNode{OwnerPolicy: &runstate.OwnerPolicyResult{Kind: runstate.PolicyApprovalBypass, Generation: row.Generation}, Result: &runstate.NodeResult{Outcome: runstate.NodeSkipped, Reason: "skip_if"}, Outcome: runstate.NodeSkipped}}, nil
}
