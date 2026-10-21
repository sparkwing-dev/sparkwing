package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
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
		if admission, ok := ctx.Value(approvalAdmissionContext{}).(NativeAdmission); ok {
			actor := store.Actor{}
			if admission.capacity != nil {
				actor = admission.capacity.Owner
			}
			return admission.withNode(ctx, input.Accepted, row, actor, func(ctx context.Context, _ wingwire.HostResources) error {
				return h.withProcess(ctx, input, spec, evaluate)
			})
		}
		return h.withProcess(ctx, input, spec, evaluate)
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
	return OwnerLayerResult{Transition: &store.NodeTransition{
		To: store.NodeSuccess, Cause: store.CauseOwnerPolicy,
		Extra: store.TransitionExtra{
			OwnerPolicy: &store.OwnerPolicyResult{Kind: store.PolicyApprovalBypass, Generation: row.Generation},
			Result:      &store.NodeResult{Outcome: store.NodeSuccess, Reason: "skip_if"},
		},
	}}, nil
}
