package localcontroller

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// SetAdmissionCancellation connects committed run cancellation to machine admission.
// Bind it before starting HTTP handlers.
func (c *localController) SetAdmissionCancellation(cancel func(context.Context, string) error) {
	c.store.SetRunCancellationObserver(func(ctx context.Context, team store.Team, runID string) error {
		if team != c.tenant.Team() || cancel == nil {
			return nil
		}
		return cancel(ctx, runID)
	})
}
