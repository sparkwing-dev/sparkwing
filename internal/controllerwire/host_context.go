package controllerwire

import (
	"context"
	"time"
)

// HostContext bounds preparation and hosting by the award's immutable authority deadlines.
func (u ClaimedUnit) HostContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := u.Binding.Deadline
	for _, bound := range []*time.Time{u.OwnerDeadline, u.ParentCapabilityExpiry} {
		if bound != nil && (deadline == nil || bound.Before(*deadline)) {
			deadline = bound
		}
	}
	if deadline != nil {
		return context.WithDeadline(ctx, *deadline)
	}
	return context.WithCancel(ctx)
}
