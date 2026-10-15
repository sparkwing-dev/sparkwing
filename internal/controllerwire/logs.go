package controllerwire

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

// LogQuery keeps an explicit zero head/tail distinct from an absent bound.
// Cursor resumes the same selection across pages and follow polls; the server
// binds it to the filters and tracks every tagged subject, including descendants.
type LogQuery struct {
	Job                  string
	Tail, Head           *int
	Lines, Grep, Cursor  string
	Since                time.Duration
	Tree                 bool
	EventsOnly, NoEvents bool
}

func (q LogQuery) Validate() error {
	if q.Tail != nil && *q.Tail < 0 || q.Head != nil && *q.Head < 0 || q.Since < 0 {
		return fmt.Errorf("log bounds and --since must be nonnegative")
	}
	if q.Tail != nil && q.Head != nil {
		return fmt.Errorf("--tail and --head cannot be combined")
	}
	if q.EventsOnly && q.NoEvents {
		return fmt.Errorf("--events-only and --no-events cannot be combined")
	}
	if q.Lines != "" {
		start, end, ok := strings.Cut(q.Lines, ":")
		a, errA := strconv.Atoi(start)
		b, errB := strconv.Atoi(end)
		if !ok || errA != nil || errB != nil || a < 1 || b < a {
			return fmt.Errorf("--lines requires an inclusive range A:B with 1 <= A <= B")
		}
	}
	return nil
}

// LogRecord is either output from a tagged subject or a lifecycle event.
// Owner logs retain their real trigger/plan/eval identity rather than a fake job.
type LogRecord struct {
	RunID   string           `json:"run_id"`
	Subject *blob.LogSubject `json:"subject,omitempty"`
	Event   string           `json:"event,omitempty"`
	JobID   string           `json:"job_id,omitempty"`
	StepID  string           `json:"step_id,omitempty"`
	Time    *time.Time       `json:"time,omitempty"`
	LineNo  int64            `json:"line_no"`
	Line    string           `json:"line"`
}

func (r LogRecord) Validate() error {
	if r.RunID == "" || (r.Subject == nil) == (r.Event == "") || r.LineNo < 0 {
		return fmt.Errorf("log record requires a run and exactly one subject or event")
	}
	if r.Subject != nil && (r.Subject.RunID != r.RunID || r.LineNo < 1) {
		return fmt.Errorf("log subject must match its run and carry a positive line number")
	}
	if r.StepID != "" && r.JobID == "" || r.Subject != nil && (r.JobID != "" || r.StepID != "") {
		return fmt.Errorf("event job and step identifiers must stay separate from output subjects")
	}
	if s := r.Subject; s != nil {
		switch s.Kind {
		case blob.LogJobAttempt:
			a := s.Attempt
			if a == nil || s.Unit != nil || a.Team != s.Team || a.RunID != s.RunID || a.NodeID == "" || a.Attempt < 1 || a.Generation < 1 {
				return fmt.Errorf("log record has an invalid job attempt subject")
			}
		case blob.LogOwnerUnit:
			u := s.Unit
			if u == nil || s.Attempt != nil || u.Generation < 1 {
				return fmt.Errorf("log record has an invalid owner subject")
			}
			switch u.Kind {
			case blob.LogTrigger, blob.LogPlan:
				if u.Source != nil {
					return fmt.Errorf("trigger and plan log subjects cannot name a source")
				}
			case blob.LogEval:
				if u.Source == nil || u.Source.NodeID == "" || u.Source.Generation < 1 {
					return fmt.Errorf("eval log subject requires a source")
				}
			default:
				return fmt.Errorf("log record has an unknown owner kind")
			}
		default:
			return fmt.Errorf("log record has an unknown subject kind")
		}
	}
	return nil
}

// Complete means persisted terminal state and final log segments are observed.
// An idle live page retains its cursor. Truncated pages require immediate reads;
// a nontruncated, incomplete page requires a later follow poll at NextCursor.
type LogPage struct {
	Records    []LogRecord `json:"records"`
	NextCursor string      `json:"next_cursor"`
	Truncated  bool        `json:"truncated"`
	Complete   bool        `json:"complete"`
}

func (p LogPage) Validate(cursor string) error {
	if p.Truncated && (p.NextCursor == "" || p.NextCursor == cursor) || !p.Complete && p.NextCursor == "" || len(p.Records) > 0 && p.NextCursor != "" && p.NextCursor == cursor {
		return fmt.Errorf("controller returned inconsistent log pagination")
	}
	for _, r := range p.Records {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type LogSearchQuery struct {
	Runs       RunQuery
	Job        string
	Pattern    string
	MaxMatches int
}

// LogSearchPage preserves a cursor over candidate runs even when none matched.
// MaxMatches is a per-subject limit; zero leaves matches uncapped.
type LogSearchPage struct {
	Matches    []LogRecord `json:"matches"`
	NextCursor string      `json:"next_cursor"`
	Truncated  bool        `json:"truncated"`
}

func (p LogSearchPage) Validate(cursor string) error {
	if p.Truncated && (p.NextCursor == "" || p.NextCursor == cursor) || !p.Truncated && p.NextCursor != "" {
		return fmt.Errorf("controller returned inconsistent log search pagination")
	}
	for _, r := range p.Matches {
		if err := r.Validate(); err != nil {
			return err
		}
		if r.Subject == nil {
			return fmt.Errorf("log search returned a lifecycle event")
		}
	}
	return nil
}

// LogReader keeps local and remote reads on the same authorized, metered routes.
type LogReader interface {
	ReadLogs(context.Context, string, LogQuery) (LogPage, error)
	SearchLogs(context.Context, LogSearchQuery) (LogSearchPage, error)
}
