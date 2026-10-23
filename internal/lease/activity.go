package lease

import (
	"context"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Identity names the exact claimed unit whose keeper can be shared.
type Identity struct {
	Kind       capability.UnitKind
	RunID      string
	NodeID     string
	Holder     string
	Generation int64
}

type activityKey struct{}

type activity struct {
	identity Identity
	mu       sync.RWMutex
	compute  func() bool
}

// bind ties preparation and execution to one unit's keeper and activity projection.
func bind(ctx context.Context, identity Identity) (context.Context, func() bool) {
	a := &activity{identity: identity}
	return context.WithValue(ctx, activityKey{}, a), func() bool {
		a.mu.RLock()
		defer a.mu.RUnlock()
		return a.compute == nil || a.compute()
	}
}

// Adopt assigns activity to an existing keeper only for its exact live unit.
func Adopt(ctx context.Context, identity Identity, compute func() bool) bool {
	a, ok := ctx.Value(activityKey{}).(*activity)
	if !ok || a.identity != identity || identity.RunID == "" || identity.Holder == "" || identity.Generation <= 0 ||
		identity.Kind != capability.UnitTrigger && identity.Kind != capability.UnitNode ||
		(identity.Kind == capability.UnitNode) != (identity.NodeID != "") || compute == nil || ctx.Err() != nil {
		return false
	}
	a.mu.Lock()
	a.compute = compute
	a.mu.Unlock()
	return true
}
