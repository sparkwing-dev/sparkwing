package runstate

import (
	"encoding/json"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// RunRow binds execution facts to the host's team identity.
type RunRow struct {
	Team string
	Execution
}

// Execution contains the shared run facts used by persistence and hosts.
type Execution struct {
	ID                string
	Number            int64
	Pipeline          string
	State             RunState
	Request           runspec.SealedRequest
	Lineage           runspec.Lineage
	Retry             *RetrySelection
	Spec              *runspec.Spec
	Plan              json.RawMessage
	PlanHash          string
	Dispatch          string
	AllowedPools      []string
	Owner             *OwnerLeaseRef
	CreatedAt         time.Time
	CancelRequestedAt *time.Time
	CancelReason      string
	StartedAt         *time.Time
	FinishedAt        *time.Time
	StateSeq          int64
	StateDigest       string
	StateActor        Actor
}

// RunSnapshot carries a consistent execution projection without persistence metadata.
type RunSnapshot struct {
	Run        RunRow
	Nodes      []NodeRow
	Expansions []AcceptedExpansionRow
	StoreNow   time.Time
}

// LocalPublication carries terminal execution state and selected output records.
type LocalPublication struct {
	RunSnapshot
	Records []blob.RecordLookupResult `json:"publication_records,omitempty"`
	Results map[string]NodeResult     `json:"publication_results,omitempty"`
}

// NodeCommandPayload carries an edge without authenticated actor or award facts.
type NodeCommandPayload struct {
	To            NodeState               `json:"to"`
	Cause         TransitionCause         `json:"cause"`
	SourceAttempt *runspec.AttemptOrdinal `json:"source_attempt,omitempty"`
	Extra         NodeCommandExtra        `json:"extra"`
}

// AcquireSlotResponse carries the admission result and queue observations.
type AcquireSlotResponse struct {
	FailureReason    string
	Kind             AcquireKind
	HolderID         string
	LeaseExpiresAt   time.Time
	SupersededIDs    []string
	PreviousCapacity int
	DriftNote        string

	// Observability for a queued arrival (Kind == AcquireQueued):
	// Position is the number of queue-policy waiters that arrived earlier,
	// QueueLength is the total queued for the key, and Holders are the slots
	// currently held. Weighted admission may backfill a later waiter that fits
	// while an earlier waiter stays queued. Zero/empty for other kinds.
	Position    int
	QueueLength int
	Holders     []ConcurrencyHolder
}

// ConcurrencyHolder reports a capacity holder without persistence metadata.
type ConcurrencyHolder struct {
	Key            string
	HolderID       string
	RunID          string
	NodeID         string
	ClaimedAt      time.Time
	QueueArrivedAt time.Time
	LeaseExpiresAt time.Time
	Superseded     bool

	// Cost is the admission weight this holder draws from the key's
	// budget; DeclaredCapacity is the capacity it declared. Both are loaded
	// for admission and the operator budget view.
	Cost             int
	DeclaredCapacity int
}
