package store

import (
	"context"
	"time"
)

// CapacityBinding makes capacity follow work across owner loss instead of following a process.
type CapacityBinding struct {
	RunID      string
	NodeID     string
	Generation int64
}

// CapacityHolder retains its own lease as proof that capacity has counted continuously.
type CapacityHolder struct {
	Key            string
	ID             string
	Binding        CapacityBinding
	Cost           int
	AcquiredAt     time.Time
	LeaseExpiresAt time.Time
	Superseded     bool
}

// CapacityWaiter keeps its arrival order only while its own lease remains live.
type CapacityWaiter struct {
	Key            string
	ID             string
	Binding        CapacityBinding
	ArrivedAt      time.Time
	LeaseExpiresAt time.Time
}

// AdoptionKind refuses to revive expired capacity; a new arrival goes through normal admission.
type AdoptionKind string

// Adoption outcomes preserve continuously retained holders and waiters only.
const (
	AdoptHolder  AdoptionKind = "holder"
	AdoptWaiter  AdoptionKind = "waiter"
	AcquireAgain AdoptionKind = "new_arrival"
)

// CapacityAdoption reports the store's liveness decision, never a caller-supplied original arrival time.
type CapacityAdoption struct {
	Kind   AdoptionKind
	Holder *CapacityHolder
	Waiter *CapacityWaiter
}

// CapacityLivenessRule describes the conditions which renew the holder's continuity lease.
type CapacityLivenessRule struct {
	Scope      string
	Conditions []string
}

// CapacityLiveness retains existing admission policy while changing who keeps capacity alive.
func CapacityLiveness() []CapacityLivenessRule {
	return []CapacityLivenessRule{
		{"node", []string{"node nonterminal", "holder lease unexpired", "not superseded", "node claim OR owner lease live"}},
		{"plan", []string{"run nonterminal", "holder lease unexpired", "not superseded", "owner lease OR any run node claim live"}},
		{"waiter", []string{"waiter lease unexpired", "bound work nonterminal", "authorized successor owner"}},
	}
}

// AdoptCapacity checks continuity under the entry lock; expired holders and waiters are removed before a fresh request.
func (t *Tenant) AdoptCapacity(ctx context.Context, key, id string, binding CapacityBinding, actor Actor) (CapacityAdoption, error) {
	panic("unimplemented: c1-data")
}

// SupersedeCapacity marks the holder superseded and requests cancellation on its bound work in the same transaction.
func (t *Tenant) SupersedeCapacity(ctx context.Context, key, holderID string, actor Actor) error {
	panic("unimplemented: c1-data")
}
