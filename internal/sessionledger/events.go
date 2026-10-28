package sessionledger

import (
	"context"
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"time"
)

// eventKind is the node event a sweep appends for every session it ended
// or could not end, so `runs status` shows what happened to the work.
const eventKind = runstate.EventStraySessionReaped

// EventAppender is the store method a sweep needs to record its outcomes.
type EventAppender interface {
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error
}

// eventPayload is the JSON body of an [eventKind] event.
type eventPayload struct {
	Reaper      string    `json:"reaper"`
	Verdict     Verdict   `json:"verdict"`
	OwnerPID    int       `json:"owner_pid"`
	LeaderPID   int       `json:"leader_pid,omitempty"`
	JobName     string    `json:"job_name,omitempty"`
	Description string    `json:"description"`
	StartedAt   time.Time `json:"started_at"`
	Error       string    `json:"error,omitempty"`
}

// RecordOutcomes appends one event per reaped or failed outcome. Live
// records are not events: nothing happened to them. Append failures are
// returned together so a caller can log them without losing the sweep.
func RecordOutcomes(ctx context.Context, st EventAppender, reaper string, outcomes []Outcome) []error {
	var errs []error
	for _, o := range outcomes {
		if o.Verdict == VerdictLive || o.Verdict == VerdictWouldReap {
			continue
		}
		p := eventPayload{
			Reaper: reaper, Verdict: o.Verdict,
			OwnerPID: o.OwnerPID, LeaderPID: o.LeaderPID, JobName: o.JobName,
			Description: o.Description, StartedAt: o.StartedAt,
		}
		if o.Err != nil {
			p.Error = o.Err.Error()
		}
		body, _ := json.Marshal(p)
		if err := st.AppendEvent(ctx, o.Run, o.Node, eventKind, body); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
