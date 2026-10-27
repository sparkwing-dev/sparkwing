package blob

import (
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// LogSubjectKind separates attempt logs from work that runs before a job exists.
type LogSubjectKind string

const (
	LogJobAttempt LogSubjectKind = "job_attempt"
	LogOwnerUnit  LogSubjectKind = "owner_unit"
)

// LogUnitKind names the persisted lease whose generation fences an owner log.
type LogUnitKind string

const (
	LogTrigger LogUnitKind = "trigger"
	LogPlan    LogUnitKind = "plan"
	LogEval    LogUnitKind = "eval"
)

// LogSource binds evaluation logs to the terminal source without requiring an attempt.
type LogSource struct {
	NodeID     string `json:"job_id"`
	Generation int64  `json:"generation"`
}

// LogUnit uses the subject's run to identify trigger/plan work. Eval additionally
// requires Source. Generation is the unit's own positive fence, not its source's.
type LogUnit struct {
	Kind       LogUnitKind `json:"kind"`
	Generation int64       `json:"generation"`
	Source     *LogSource  `json:"source,omitempty"`
}

// LogSubject is shared by signing, segment commits, live pushes and reads.
// JobAttempt requires Attempt only, matching Team and RunID. OwnerUnit requires
// Unit only; eval requires Source and trigger/plan forbid it. Generations and
// attempt ordinals are positive; a materialized eval source can be generation zero.
type LogSubject struct {
	Kind    LogSubjectKind           `json:"kind"`
	Team    string                   `json:"team"`
	RunID   string                   `json:"run_id"`
	Attempt *runspec.AttemptIdentity `json:"attempt,omitempty"`
	Unit    *LogUnit                 `json:"unit,omitempty"`
}

// Validate rejects malformed tagged subjects before authority or object-key lookup.
func (subject LogSubject) Validate() error {
	if subject.Team == "" || subject.RunID == "" {
		return fmt.Errorf("log subject requires team and run")
	}
	switch subject.Kind {
	case LogJobAttempt:
		if subject.Unit == nil && subject.Attempt != nil && validAttempt(*subject.Attempt) && subject.Attempt.Team == subject.Team && subject.Attempt.RunID == subject.RunID {
			return nil
		}
	case LogOwnerUnit:
		if subject.Attempt != nil || subject.Unit == nil || subject.Unit.Generation <= 0 {
			break
		}
		switch subject.Unit.Kind {
		case LogTrigger, LogPlan:
			if subject.Unit.Source == nil {
				return nil
			}
		case LogEval:
			if s := subject.Unit.Source; s != nil && s.NodeID != "" && s.Generation >= 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("invalid log subject identity")
}

// LogSigningRequest requests one immutable PUT bound to its exact stored bytes.
// Index zero selects the final manifest; positive indexes select segments. The
// controller resolves the run's retention class and object key after checking
// the current binding and generation. Callers cannot choose an upload namespace.
type LogSigningRequest struct {
	Subject LogSubject `json:"subject"`
	Index   int64      `json:"index"`
	Size    int64      `json:"size"`
	Digest  Digest     `json:"digest"`
}

// LogUploadURL permits one create-only PUT of the requested bytes; it grants no
// read authority. Headers must accompany the PUT, including its signed length,
// checksum and create-only condition. The URL expires after the 15-minute PUT lifetime.
type LogUploadURL struct {
	Subject        LogSubject        `json:"subject"`
	RetentionClass string            `json:"retention_class"`
	Index          int64             `json:"index"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers,omitempty"`
	ExpiresAt      time.Time         `json:"expires_at"`
	// Committed reports the slot's object is already committed, so the uploader has nothing to send and no URL.
	Committed bool `json:"committed,omitempty"`
}

// LogReadRequest resumes by byte offset within exactly one subject and generation.
// Reads reauthorize at the controller, including terminal subjects. Segment fallback
// covers ring gaps and stalled progress; no signed download URL is returned.
type LogReadRequest struct {
	Subject LogSubject `json:"subject"`
	Offset  int64      `json:"offset"`
	Follow  bool       `json:"follow"`
}

// LogUploadCheck identifies an uncertain immutable PUT without accepting an object
// key. Index zero selects the final manifest; positive indexes select segments.
// Size and Digest describe stored bytes, including compression for segments.
type LogUploadCheck struct {
	Subject LogSubject `json:"subject"`
	Index   int64      `json:"index"`
	Size    int64      `json:"size"`
	Digest  Digest     `json:"digest"`
}
