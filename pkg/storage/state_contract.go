package storage

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// StateStore is the tenant-scoped host contract; status writes use transitions and source inputs use the two seals.
// A host borrows this handle and does not close the shared store. Claims and deployment maintenance stay unscoped.
type StateStore interface {
	AdmitRun(context.Context, store.IntakeRequest) (store.AdmissionResult, error)
	AcceptPlan(context.Context, store.PlanAcceptance) (store.AcceptedPlan, error)
	AcceptExpansion(context.Context, store.ExpansionSubmission) (store.AcceptedExpansion, error)
	TransitionNode(context.Context, store.NodeTransition) (store.Applied, error)
	TransitionRun(context.Context, store.RunTransition) (store.Applied, error)
	FinishRunAndUnit(context.Context, store.FinishRunRequest) (store.Applied, error)
	ReadRun(context.Context, string) (store.RunRow, error)
	ReadNode(context.Context, store.NodeRef) (store.NodeRow, error)
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	ReadStateEvents(context.Context, string, int64, int) ([]store.StateEvent, error)
	ReconcileNode(context.Context, store.ReconcileRequest) (store.ReconciledNode, error)
	RenewOwner(context.Context, store.Actor) (store.LeaseBeat, error)
	RenewNodeClaim(context.Context, store.NodeRef, store.Actor) (store.LeaseBeat, error)
	RecordStep(context.Context, store.StepReport) error
	WriteAnnotation(context.Context, store.AnnotationWrite) error
	WriteSummary(context.Context, store.SummaryWrite) error
	RecordSegments(context.Context, store.SegmentCommit) error
}

var _ StateStore = (*store.Tenant)(nil)
