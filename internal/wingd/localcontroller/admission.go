package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (c *localController) Ready() error {
	c.mu.Lock()
	ready := !c.closed && c.lease.Name != ""
	c.mu.Unlock()
	if !ready {
		return errors.New("wingd: local store is not reconciled and open")
	}
	return c.State.Ready()
}

func (c *localController) IsRunTerminal(runID string) (bool, error) {
	row, err := c.tenant.ReadRun(context.Background(), runID)
	return terminal(row.State), err
}

func (c *localController) FinalizeRun(runID string) {
	if err := c.FinalizeCancelledRuns([]string{runID}, "admission connection lost"); err != nil {
		slog.Error("wingd: cancel disconnected run", "run", runID, "error", err)
	}
}

func (c *localController) FinalizeCancelledRuns(runIDs []string, reason string) error {
	c.mu.Lock()
	lease, closed := c.lease, c.closed
	c.mu.Unlock()
	if closed || lease.Name == "" {
		return errors.New("wingd: admission cancellation requires a reconciled open controller")
	}
	actor := store.Actor{Kind: store.ActorReaper, Owner: store.OwnerLeaseRef{
		Kind: store.OwnerController, Name: lease.Name, Holder: lease.Holder, Generation: lease.Generation,
	}}
	var result error
	for _, runID := range runIDs {
		if err := c.cancelRun(runID, reason, actor); err != nil {
			result = errors.Join(result, fmt.Errorf("wingd: cancel run %s: %w", runID, err))
		}
	}
	return result
}

func (c *localController) cancelRun(runID, reason string, actor store.Actor) error {
	ctx := context.Background()
	row, err := c.tenant.ReadRun(ctx, runID)
	if err != nil || terminal(row.State) || row.CancelRequestedAt != nil {
		return err
	}
	_, err = c.tenant.TransitionRun(ctx, store.RunTransition{
		RunID: runID, To: store.RunCancelled, Cause: store.RunCancel, Actor: actor, Reason: reason,
	})
	if err != nil {
		var transition *store.ErrIllegalTransition
		if errors.As(err, &transition) && transition.Reason == store.ReasonTerminal {
			return nil
		}
	}
	return err
}

func terminal(state store.RunState) bool {
	return state == store.RunSuccess || state == store.RunFailed || state == store.RunCancelled
}
