package localcontroller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func (c *localController) Ready() error {
	c.mu.Lock()
	ready := !c.closed && c.reconciled
	c.mu.Unlock()
	if !ready {
		return errors.New("wingd: local store is not reconciled and open")
	}
	return c.State.Ready()
}

func (c *localController) IsRunTerminal(runID wingwire.RunID) (bool, error) {
	if runID.Remote() {
		return false, nil
	}
	row, err := c.tenant.ReadRun(context.Background(), string(runID))
	return terminal(row.State), err
}

func (c *localController) FinalizeRun(runID string) {
	if err := c.FinalizeCancelledRuns([]string{runID}, "admission connection lost"); err != nil {
		slog.Error("wingd: cancel disconnected run", "run", runID, "error", err)
	}
}

func (c *localController) FinalizeCancelledRuns(runIDs []string, reason string) error {
	c.mu.Lock()
	reconciled, closed := c.reconciled, c.closed
	c.mu.Unlock()
	if closed || !reconciled {
		return errors.New("wingd: admission cancellation requires a reconciled open controller")
	}
	actor := runstate.Actor{Kind: runstate.ActorReaper, Owner: runstate.OwnerLeaseRef{Kind: runstate.OwnerController}}
	var result error
	for _, runID := range runIDs {
		if wingwire.HolderRef(runID).RunID().Remote() {
			continue
		}
		if err := c.cancelRun(runID, reason, actor); err != nil {
			result = errors.Join(result, fmt.Errorf("wingd: cancel run %s: %w", runID, err))
		}
	}
	return result
}

func (c *localController) cancelRun(runID, reason string, actor runstate.Actor) error {
	ctx := context.Background()
	row, err := c.tenant.ReadRun(ctx, runID)
	if err != nil || terminal(row.State) || row.CancelRequestedAt != nil {
		return err
	}
	_, err = c.tenant.TransitionRun(ctx, runstate.RunTransition{
		RunID: runspec.RunID(runID), To: runstate.RunCancelled, Cause: runstate.RunCancel, Actor: actor, Reason: reason,
	})
	if err != nil {
		var transition *store.ErrIllegalTransition
		if errors.As(err, &transition) && transition.Reason == store.ReasonTerminal {
			return nil
		}
	}
	return err
}

func terminal(state runstate.RunState) bool {
	return state == runstate.RunSuccess || state == runstate.RunDryRun || state == runstate.RunFailed || state == runstate.RunCancelled
}
