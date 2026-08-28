package admission

import "errors"

var (
	ErrInvalidConfig = errors.New("admission: invalid config")

	ErrInvalidRequest = errors.New("admission: invalid request")

	ErrNeverAdmissible = errors.New("admission: request can never be admitted")

	ErrDuplicateID = errors.New("admission: participant id already holds or waits")

	ErrUnknownLease = errors.New("admission: unknown lease")

	ErrUnknownMember = errors.New("admission: unknown lease member")

	ErrUnknownToken = errors.New("admission: unknown re-attach token")

	ErrInvalidHeadroom = errors.New("admission: invalid headroom")

	ErrInvalidResize = errors.New("admission: invalid resize")

	ErrInvalidSnapshot = errors.New("admission: invalid snapshot")
)

type Policy string

const (
	PolicyQueue Policy = "queue"

	PolicyFail Policy = "fail"

	PolicySkip Policy = "skip"

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
}
