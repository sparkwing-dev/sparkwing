package controllerwire

import (
	"context"
	"time"
)

// RunQuery selects a page of public run details. ParentRunID is an exact parent
// identity, not a prefix. Cursor is opaque and retains the same filters.
type RunQuery struct {
	ParentRunID string
	Limit       int
	Cursor      string
}

type RunPage struct {
	Runs       []RunDetail `json:"runs"`
	NextCursor string      `json:"next_cursor"`
	Truncated  bool        `json:"truncated"`
}

// StepDetail contains display state only. Step inputs and execution credentials
// cannot enter the public observation boundary.
type StepDetail struct {
	JobID      string     `json:"job_id"`
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Attempt    int64      `json:"attempt"`
	Status     string     `json:"status"`
	Outcome    string     `json:"outcome,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type RunSteps struct {
	Steps []StepDetail `json:"steps"`
}

// Receipt is calculated by the controller from persisted run and billing facts.
// Clients preserve its hash and settlement; they never estimate a replacement.
// Identity excludes hashes of secret arguments and uses the accepted seal hashes.
type Receipt struct {
	RunID      string          `json:"run_id"`
	Pipeline   string          `json:"pipeline"`
	GitSHA     string          `json:"git_sha"`
	Status     string          `json:"status"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	DurationMS int64           `json:"duration_ms"`
	Identity   ReceiptIdentity `json:"identity"`
	Steps      []ReceiptStep   `json:"steps"`
	Cost       ReceiptCost     `json:"cost"`
	ReceiptSHA string          `json:"receipt_sha"`
}

type ReceiptIdentity struct {
	PipelineVersionHash string            `json:"pipeline_version_hash"`
	InputsHash          string            `json:"inputs_hash"`
	PlanHash            string            `json:"plan_hash"`
	OutputsHash         map[string]string `json:"outputs_hash"`
}

type ReceiptStep struct {
	ID         string `json:"id"`
	JobID      string `json:"job_id"`
	DurationMS int64  `json:"duration_ms"`
	Outcome    string `json:"outcome"`
	SkipReason string `json:"skip_reason,omitempty"`
}

type ReceiptCost struct {
	Currency     string `json:"currency"`
	ComputeCents int64  `json:"compute_cents"`
	RateSource   string `json:"rate_source"`
	Settled      bool   `json:"settled"`
}

// RunReader supplies public observations through HTTP or api.sock. It shares
// ReadRun with Controller without granting engine mutation capabilities.
type RunReader interface {
	ReadRun(context.Context, string) (RunDetail, error)
	ListRuns(context.Context, RunQuery) (RunPage, error)
	ReadSteps(context.Context, string) (RunSteps, error)
	ReadReceipt(context.Context, string) (Receipt, error)
}
