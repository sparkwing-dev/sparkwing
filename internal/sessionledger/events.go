package sessionledger

import (
	"context"
	"encoding/json"
	"time"
)

// EventKind is the node event a sweep appends for every session it ended
// or could not end, so `runs status` shows what happened to the work.
const EventKind = "stray_session_reaped"

// EventAppender is the store method a sweep needs to record its outcomes.
type EventAppender interface {
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) (int64, error)
}

// EventPayload is the JSON body of an [EventKind] event.
type EventPayload struct {
	Reaper    string    `json:"reaper"`
	Verdict   Verdict   `json:"verdict"`
	OwnerPID  int       `json:"owner_pid"`
	LeaderPID int       `json:"leader_pid,omitempty"`
	JobName   string    `json:"job_name,omitempty"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
	Error     string    `json:"error,omitempty"`
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
		p := EventPayload{
			Reaper: reaper, Verdict: o.Verdict,
			OwnerPID: o.Record.OwnerPID, LeaderPID: o.Record.Handle.LeaderPID, JobName: o.Record.Handle.JobName,
			Command: o.Record.Command, StartedAt: o.Record.StartedAt,
		}
		if o.Err != nil {
			p.Error = o.Err.Error()
		}
		body, _ := json.Marshal(p)
		if _, err := st.AppendEvent(ctx, o.Record.Run, o.Record.Node, EventKind, body); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Acted reports whether any outcome ended or failed to end a session, so a
// caller can skip opening a store when there is nothing to record.
func Acted(outcomes []Outcome) bool {
	for _, o := range outcomes {
		if o.Verdict == VerdictReaped || o.Verdict == VerdictFailed {
			return true
		}
	}
	return false
}
