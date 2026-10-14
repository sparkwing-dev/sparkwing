package nodehost

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RemoteOwnerStore projects shared controller responses into the same owner
// interfaces as a local Tenant. It never introduces another decision policy.
type RemoteOwnerStore struct{ Controller controllerwire.Controller }

func (s *RemoteOwnerStore) ReconcileNode(ctx context.Context, request store.ReconcileRequest) (store.ReconciledNode, error) {
	panic("unimplemented: controller-wire")
}

func (s *RemoteOwnerStore) AcceptExpansion(ctx context.Context, submission store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	panic("unimplemented: controller-wire")
}

// Schedule reconstructs only the store's scheduling projection; the owner calls
// the same Decide used locally. HTTP never serializes Actor or caller authority.
func (s *RemoteOwnerStore) Schedule(ctx context.Context, runID string) (store.ScheduleSnapshot, error) {
	panic("unimplemented: controller-wire")
}

var (
	_ Reconciler     = (*RemoteOwnerStore)(nil)
	_ ExpansionStore = (*RemoteOwnerStore)(nil)
)
