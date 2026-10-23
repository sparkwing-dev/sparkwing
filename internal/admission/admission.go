package admission

import "errors"

// DefaultChargeCores charges unhinted execution against one core.
const DefaultChargeCores = 1.0

var (
	errInvalidConfig = errors.New("admission: invalid config")

	ErrInvalidRequest = errors.New("admission: invalid request")

	ErrNeverAdmissible = errors.New("admission: request can never be admitted")

	ErrDuplicateID = errors.New("admission: participant id already holds or waits")

	errUnknownLease = errors.New("admission: unknown lease")

	errUnknownMember = errors.New("admission: unknown lease member")

	errUnknownToken = errors.New("admission: unknown re-attach token")

	errInvalidHeadroom = errors.New("admission: invalid headroom")

	errInvalidResize = errors.New("admission: invalid resize")

	errInvalidSnapshot = errors.New("admission: invalid snapshot")
)

type Policy string

const (
	PolicyQueue Policy = "queue"

	policyFail Policy = "fail"

	policySkip Policy = "skip"

	PolicyCancelOthers Policy = "cancel_others"
)

type SemaphoreClaim struct {
	Key string

	Capacity int

	Cost int

	Policy Policy
}

type Request struct {
	ID string

	OwnerID string

	Cores float64

	SoftCores bool

	StrictCores bool

	MemoryBytes uint64

	Semaphores []SemaphoreClaim

	Priority int

	Class WorkloadClass

	ExpectedP99MS int64

	BurstCores bool
}

type LeaseID string

type Lease struct {
	ID    LeaseID
	Token string
}

type DecisionKind string

const (
	DecisionGranted DecisionKind = "granted"

	DecisionQueued DecisionKind = "queued"

	DecisionFailed DecisionKind = "failed"

	DecisionSkipped DecisionKind = "skipped"
)

type Decision struct {
	Kind DecisionKind

	Lease Lease

	Position int

	Key string

	Evicted []LeaseID
}

type EventKind string

const (
	EventGranted EventKind = "granted"

	EventQueued EventKind = "queued"

	EventPromoted EventKind = "promoted"

	EventBackfilled EventKind = "backfilled"

	EventEvicted EventKind = "evicted"

	EventReleased EventKind = "released"

	EventReprioritized EventKind = "reprioritized"

	// EventOrphaned names, as RequestID, an attached run whose ancestor Lease
	// was released or evicted.
	EventOrphaned EventKind = "orphaned"
)

type Event struct {
	Seq uint64 `json:"seq"`

	Kind EventKind `json:"kind"`

	RequestID string `json:"request_id"`

	Lease LeaseID `json:"lease,omitempty"`

	Position int `json:"position,omitempty"`

	Key string `json:"key,omitempty"`

	SupersededBy LeaseID `json:"superseded_by,omitempty"`

	BypassedBy string `json:"bypassed_by,omitempty"`

	BackfillCount uint64 `json:"backfill_count,omitempty"`

	BackfillDelayMS int64 `json:"backfill_delay_ms,omitempty"`

	Priority int `json:"priority,omitempty"`
}
