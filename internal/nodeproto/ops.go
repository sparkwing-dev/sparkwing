package nodeproto

import (
	"encoding/json"
	"fmt"
	"time"
)

// Op names a request the host sends.
type Op string

// The requests an SDK process answers.
const (
	OpDescribe Op = "describe"
	OpPlan     Op = "plan"
	OpRunNode  Op = "run_node"
	OpRunStep  Op = "run_step"
	OpEval     Op = "eval"
)

// ErrorCode classifies a failed reply so the host can act on it without
// parsing the message. An SDK may send a code this build does not know; the
// host treats it as a plain failure.
type ErrorCode string

// The failure codes an SDK sends.
const (
	CodeBadRequest     ErrorCode = "bad_request"
	CodeUnknownOp      ErrorCode = "unknown_op"
	CodeUnknownNode    ErrorCode = "unknown_node"
	CodeUnknownClosure ErrorCode = "unknown_closure"
	CodePlanDrift      ErrorCode = "plan_drift"
)

// Request is one line the host writes to the SDK process's stdin. Which
// fields are set depends on Op; [Request.Validate] checks them.
type Request struct {
	ID string `json:"id"`
	Op Op     `json:"op"`

	// Pipeline, Args and Run are the plan request's inputs.
	Pipeline string            `json:"pipeline,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
	Run      *RunContext       `json:"run,omitempty"`

	// Node names the node for run_node and run_step.
	Node string `json:"node,omitempty"`
	// Attempt is the 1-based attempt number of a run_node.
	Attempt int `json:"attempt,omitempty"`
	// SpecHash is the node's hash in the accepted plan. An SDK whose own plan
	// hashes the node differently refuses run_node with plan_drift.
	SpecHash string `json:"spec_hash,omitempty"`
	// Step names the step for run_step.
	Step string `json:"step,omitempty"`
	// Options are the run options pipeline code observes, for run_node and
	// run_step.
	Options *Options `json:"options,omitempty"`

	// Closure is the plan's id of the closure an eval runs, and Input its
	// argument when the closure kind takes one.
	Closure string          `json:"closure,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
}

// RunContext is what plan code reads about its run. The host sends it with
// the plan request so the SDK never reads the run from a controller.
type RunContext struct {
	RunID     string    `json:"run_id"`
	Git       *Git      `json:"git,omitempty"`
	Trigger   *Trigger  `json:"trigger,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	NoCache   bool      `json:"no_cache,omitempty"`
	DryRun    bool      `json:"dry_run,omitempty"`
}

// Git is the trigger-time snapshot of the run's checkout.
type Git struct {
	SHA           string `json:"sha,omitempty"`
	Branch        string `json:"branch,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Repo          string `json:"repo,omitempty"`
	RepoURL       string `json:"repo_url,omitempty"`
}

// Trigger describes what started the run.
type Trigger struct {
	Source string `json:"source,omitempty"`
}

// Options are the run options that reach pipeline code; the host applies
// every other run option itself.
type Options struct {
	DryRun bool `json:"dry_run,omitempty"`
}

// Validate reports whether r carries the fields its op needs. The error is an
// [*Error] whose code an SDK can send back as is: unknown_op for an op this
// build does not answer, bad_request for anything else.
func (r Request) Validate() error {
	bad := func(format string, args ...any) error {
		return &Error{Code: CodeBadRequest, Message: fmt.Sprintf(format, args...)}
	}
	if r.ID == "" {
		return bad("request has no id")
	}
	switch r.Op {
	case OpDescribe:
	case OpPlan:
		if r.Pipeline == "" {
			return bad("plan names no pipeline")
		}
		if r.Run == nil || r.Run.RunID == "" {
			return bad("plan carries no run context")
		}
	case OpRunNode:
		if r.Node == "" {
			return bad("run_node names no node")
		}
		if r.Attempt < 1 {
			return bad("run_node attempt %d is not 1-based", r.Attempt)
		}
	case OpRunStep:
		if r.Node == "" || r.Step == "" {
			return bad("run_step needs a node and a step")
		}
	case OpEval:
		if r.Closure == "" {
			return bad("eval names no closure")
		}
	default:
		return &Error{Code: CodeUnknownOp, Message: fmt.Sprintf("unknown op %q", r.Op)}
	}
	return nil
}

// ParseRequest decodes one stdin line. A line that is not a JSON object fails
// with [ErrMalformed]; a request that decodes but fails [Request.Validate] is
// returned with that error, so the SDK can still answer its id.
func ParseRequest(line []byte) (Request, error) {
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return Request{}, fmt.Errorf("%w: request: %w", ErrMalformed, err)
	}
	return r, r.Validate()
}

// Outcome is how a node or step ended.
type Outcome string

// The outcomes a run_node or run_step reply carries.
const (
	OutcomeSuccess   Outcome = "success"
	OutcomeFailed    Outcome = "failed"
	OutcomeSkipped   Outcome = "skipped"
	OutcomeCancelled Outcome = "cancelled"
)

func validOutcome(o Outcome) error {
	switch o {
	case OutcomeSuccess, OutcomeFailed, OutcomeSkipped, OutcomeCancelled:
		return nil
	}
	return fmt.Errorf("%w: unknown outcome %q", ErrMalformed, o)
}

// RunNodeResult is the result of a successful run_node reply. A node body
// that returns an error is still a successful reply, with outcome failed;
// a failed reply means the SDK could not run the node at all.
type RunNodeResult struct {
	Outcome Outcome         `json:"outcome"`
	Error   string          `json:"error,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
}

// Validate checks the outcome.
func (r RunNodeResult) Validate() error { return validOutcome(r.Outcome) }

// RunStepResult is the result of a successful run_step reply.
type RunStepResult struct {
	Outcome Outcome `json:"outcome"`
	Error   string  `json:"error,omitempty"`
}

// Validate checks the outcome.
func (r RunStepResult) Validate() error { return validOutcome(r.Outcome) }

// EvalResult is the result of a successful eval reply. Value's type depends
// on the closure's kind: a boolean for skip_if and verify, a string for
// cache_key, null for before_run and after_run, and an array of node
// documents for a generator.
type EvalResult struct {
	Value json.RawMessage `json:"value"`
}
