package controllerwire

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// QueueGroup retains controller ordering and lease facts for one concurrency group.
// Positions are one-based. Resource observations do not grant admission authority.
type QueueGroup struct {
	Group             string             `json:"group"`
	Capacity          int                `json:"capacity"`
	EffectiveCapacity int                `json:"effective_capacity"`
	UsedCost          int                `json:"used_cost"`
	Holders           []QueueGroupHolder `json:"holders"`
	Waiters           []QueueGroupWaiter `json:"waiters"`
}

// QueueGroupHolder identifies an existing lease, including superseded holders
// whose work is draining after replacement capacity was granted.
type QueueGroupHolder struct {
	HolderID       string     `json:"holder_id"`
	RunID          string     `json:"run_id"`
	JobID          string     `json:"job_id,omitempty"`
	ClaimedAt      time.Time  `json:"claimed_at"`
	QueueArrivedAt *time.Time `json:"queue_arrived_at,omitempty"`
	LeaseExpiresAt time.Time  `json:"lease_expires_at"`
	Superseded     bool       `json:"superseded"`
	Cost           int        `json:"cost"`
}

// QueueGroupWaiter retains the admission owner's arrival and queue position.
type QueueGroupWaiter struct {
	BlockingReason string    `json:"blocking_reason,omitempty"`
	RunID          string    `json:"run_id"`
	JobID          string    `json:"job_id,omitempty"`
	ArrivedAt      time.Time `json:"arrived_at"`
	Policy         string    `json:"policy"`
	Cost           int       `json:"cost"`
	Position       int       `json:"position"`
}

// QueuePriorityWrite chooses an exact local admission priority, including
// zero, or a rank relative to the other queued runs. RunID accepts a stable ID
// or team number.
type QueuePriorityWrite struct {
	RunID    string `json:"run_id"`
	Priority *int   `json:"priority,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

// Validate rejects ambiguous rank changes before any mutation.
func (w QueuePriorityWrite) Validate() error {
	if !validQueueSelector(w.RunID) {
		return errors.New("priority requires a run")
	}
	if w.Priority != nil && w.Mode == "" {
		return nil
	}
	if w.Priority == nil && (w.Mode == "front" || w.Mode == "back") {
		return nil
	}
	return errors.New("priority requires either an integer or front/back")
}

// QueuePriorityResult carries the canonical run ID, committed rank and
// positions. An existing holder keeps its lease.
type QueuePriorityResult struct {
	RunID string `json:"run_id"`
	wingwire.SetPriorityAck
	PreviousPosition int `json:"previous_position,omitempty"`
}

// QueueManager uses the same authenticated routes locally and remotely.
// Machine admission changes keep the unchanged d.sock SetPriority operation.
type QueueManager interface {
	ReadQueue(context.Context) (wingwire.QueueState, error)
	ReadQueueGroup(context.Context, string) (QueueGroup, error)
}

// ValidateQueueGroupName accepts opaque group names while refusing whole-name
// dot segments and control characters that cannot be selected safely over HTTP.
func ValidateQueueGroupName(group string) error {
	if !validQueueSelector(group) {
		return errors.New("queue group is required")
	}
	if group == "." || group == ".." {
		return errors.New("queue group cannot be a path dot segment")
	}
	return nil
}

func validQueueSelector(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsFunc(s, unicode.IsControl)
}
