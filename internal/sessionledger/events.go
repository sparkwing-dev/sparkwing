package sessionledger

import (
	"context"
	"encoding/json"
	"time"
)

// eventKind is the node event a sweep appends for every session it ended
// or could not end, so `runs status` shows what happened to the work.
const eventKind = "stray_session_reaped"

// EventAppender is the store method a sweep needs to record its outcomes.
type EventAppender interface {
	AppendEvent(ctx context.Context, runID, nodeID, kind string, payload []byte) error
}

// eventPayload is the JSON body of an [eventKind] event.
type eventPayload struct {
	Reaper    string    `json:"reaper"`
	Verdict   Verdict   `json:"verdict"`
	OwnerPID  int       `json:"owner_pid"`
	LeaderPID int       `json:"leader_pid,omitempty"`
	JobName   string    `json:"job_name,omitempty"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
	Error     string    `json:"error,omitempty"`
}

type commandMaskKey struct{}

// MaskCommand returns command with the context's secrets hidden, or
// unchanged when the context carries no mask.
func MaskCommand(ctx context.Context, command string) string {
	if mask, ok := ctx.Value(commandMaskKey{}).(func(string) string); ok && mask != nil {
		return mask(command)
	}
	return command
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
			OwnerPID: o.Record.OwnerPID, LeaderPID: o.Record.Handle.LeaderPID, JobName: o.Record.Handle.JobName,
			Command: o.Record.Command, StartedAt: o.Record.StartedAt,
		}
		if o.Err != nil {
			p.Error = o.Err.Error()
		}
		body, _ := json.Marshal(p)
		if err := st.AppendEvent(ctx, o.Record.Run, o.Record.Node, eventKind, body); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
