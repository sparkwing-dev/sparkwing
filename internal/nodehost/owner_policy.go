package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// approvalSkipIf evaluates an approval-only guard; executable jobs evaluate under their award.
func (h *OwnerEvaluator) approvalSkipIf(ctx context.Context, input EvaluationInput) (skipEvaluation, error) {
	spec, row, job, err := h.accepted(ctx, input)
	if err == nil && (job.Approval == nil || row.Status != store.StatusPending || row.ReadyAt != nil || row.ClaimedBy != "" || row.Outcome != "") {
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
	var job nodeproto.JobSpec
	if err := json.Unmarshal(row.Spec, &job); err != nil {
		return OwnerLayerResult{}, err
	}
	if job.Approval == nil {
		return OwnerLayerResult{}, nil
	}
	result, err := h.approvalSkipIf(ctx, input)
	if err != nil {
		var refusal *apierr.Error
		if context.Cause(ctx) != nil || store.IsProtocolErr(err) || errors.As(err, &refusal) && (refusal.Code == apierr.Forbidden || refusal.Code == apierr.Unauthenticated || refusal.Code == apierr.LockHeld) {
			return OwnerLayerResult{}, err
		}
		return OwnerLayerResult{Transition: store.OwnerPolicyNode{Outcome: store.NodeFailed, OwnerPolicy: &store.OwnerPolicyResult{Kind: store.PolicyApprovalFail, Generation: row.Generation}, Result: &store.NodeResult{Outcome: store.NodeFailed, Reason: string(apierr.SDKError), MaskedError: boundedNodeErrText(h.deps.Mask(err.Error()))}}}, nil
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
	return OwnerLayerResult{Transition: store.OwnerPolicyNode{OwnerPolicy: &store.OwnerPolicyResult{Kind: store.PolicyApprovalBypass, Generation: row.Generation}, Result: &store.NodeResult{Outcome: store.NodeSkipped, Reason: "skip_if"}, Outcome: store.NodeSkipped}}, nil
}
