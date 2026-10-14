package nodeproto

import (
	"encoding/json"
	"fmt"
	"time"
)

// OpOld names a request the host sends.
type OpOld string

// The requests an SDK process answers.
const (
	OpDescribeOld OpOld = "describe"
	OpPlanOld     OpOld = "plan"
	OpRunNodeOld  OpOld = "run_node"
	OpRunStepOld  OpOld = "run_step"
	OpEvalOld     OpOld = "eval"
	// OpCancelOld cancels the run_step in flight for a node and step; that
	// run_step still gets its own reply.
	OpCancelOld OpOld = "cancel"
)

// ErrorCode classifies why an SDK could not do what a request asked, or why
// the host failed an attempt over the protocol. The host records it as the
// node's failure reason.
type ErrorCode string

// The codes an SDK sends in a failed reply. A missing code, or one this build
// does not know, is recorded as [CodeSDKError].
const (
	CodeUnknownOp       ErrorCode = "unknown_op"
	CodeUnknownPipeline ErrorCode = "unknown_pipeline"
	CodeUnknownNode     ErrorCode = "unknown_node"
	CodeUnknownStep     ErrorCode = "unknown_step"
	CodeUnknownClosure  ErrorCode = "unknown_closure"
	CodeNoPlan          ErrorCode = "no_plan"
	CodeBadRequest      ErrorCode = "bad_request"
	CodeInvalidArgs     ErrorCode = "invalid_args"
	CodePlanFailed      ErrorCode = "plan_failed"
)

// The codes only the host raises; an SDK never sends them.
const (
	CodePlanDrift        ErrorCode = "plan_drift"
	CodePlanInvalid      ErrorCode = "plan_invalid"
	CodeProtocolMismatch ErrorCode = "protocol_mismatch"
	CodeProtocolError    ErrorCode = "protocol_error"
	CodeSDKExited        ErrorCode = "sdk_exited"
	CodeFramesClosed     ErrorCode = "frames_closed"
	CodeSDKError         ErrorCode = "sdk_error"
)

// RequestOld is one line the host writes to the SDK process's request pipe. Which
// fields are set depends on OpOld; [RequestOld.Validate] checks them.
type RequestOld struct {
	ID string `json:"id"`
	Op OpOld  `json:"op"`

	// Pipeline, Args and Run are the plan request's inputs. Args is sent
	// even when empty, so a plan request sets it to a non-nil map.
	Pipeline string            `json:"pipeline,omitempty"`
	Args     map[string]string `json:"args,omitzero"`
	Run      *RunContextOld    `json:"run,omitempty"`

	// Node names the node for run_node, run_step and cancel.
	Node string `json:"node,omitempty"`
	// Step names the step for run_step and cancel.
	Step string `json:"step,omitempty"`
	// Attempt is the 1-based attempt number of run_node and run_step.
	Attempt int `json:"attempt,omitempty"`
	// Failure is set only for an on_failure node, so its recovery body can
	// branch on how its parent failed.
	Failure *Failure `json:"failure,omitempty"`

	// Closure is the plan's id of the closure an eval runs, and Input its
	// argument when the closure kind takes one.
	Closure string          `json:"closure,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
}

// RunContextOld is what plan code reads about its run. The host sends it with
// the plan request so the SDK never reads the run from a controller.
type RunContextOld struct {
	RunID     string    `json:"run_id"`
	Git       *Git      `json:"git,omitempty"`
	Trigger   *Trigger  `json:"trigger,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	NoCache   bool      `json:"no_cache,omitempty"`
	DryRun    bool      `json:"dry_run,omitempty"`
}

// Git is the trigger-time snapshot of the run's checkout.
type Git struct {
	SHA           string `json:"sha"`
	Branch        string `json:"branch"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Repo          string `json:"repo,omitempty"`
	RepoURL       string `json:"repo_url,omitempty"`
}

// Trigger describes what started the run: Source is manual, push, schedule
// or webhook.
type Trigger struct {
	Source      string       `json:"source"`
	User        string       `json:"user,omitempty"`
	PullRequest *PullRequest `json:"pull_request,omitempty"`
}

// PullRequest is the pull request a webhook run was triggered for.
type PullRequest struct {
	Number  int    `json:"number"`
	Action  string `json:"action,omitempty"`
	BaseRef string `json:"base_ref,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	HeadRef string `json:"head_ref,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
}

// Failure says how the parent of an on_failure node failed: Stage is action
// or verify.
type Failure struct {
	Node    string `json:"node"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// Validate reports whether r carries the fields its op needs. The error is an
// [*Error] whose code an SDK can send back as is: unknown_op for an op this
// build does not answer, bad_request for anything else.
func (r RequestOld) Validate() error {
	bad := func(format string, args ...any) error {
		return &Error{Code: CodeBadRequest, Message: fmt.Sprintf(format, args...)}
	}
	if r.ID == "" {
		return bad("request has no id")
	}
	switch r.Op {
	case OpDescribeOld:
	case OpPlanOld:
		if r.Pipeline == "" || r.Args == nil {
			return bad("plan needs a pipeline and args")
		}
		if r.Run == nil || r.Run.RunID == "" {
			return bad("plan carries no run context")
		}
		if t := r.Run.Trigger; t != nil && !oneOf(t.Source, "manual", "push", "schedule", "webhook") {
			return bad("unknown trigger source %q", t.Source)
		}
	case OpRunNodeOld, OpRunStepOld:
		if r.Node == "" || (r.Op == OpRunStepOld) != (r.Step != "") {
			return bad("%s names no node or step", r.Op)
		}
		if r.Attempt < 1 {
			return bad("%s attempt %d is not 1-based", r.Op, r.Attempt)
		}
		if f := r.Failure; f != nil && (f.Node == "" || !oneOf(f.Stage, "action", "verify")) {
			return bad("failure needs a node and a stage of action or verify")
		}
	case OpCancelOld:
		if r.Node == "" || r.Step == "" {
			return bad("cancel names no node or step")
		}
	case OpEvalOld:
		if r.Closure == "" {
			return bad("eval names no closure")
		}
		if !isNull(r.Input) && !isObject(r.Input) {
			return bad("eval input is not an object")
		}
	default:
		return &Error{Code: CodeUnknownOp, Message: fmt.Sprintf("unknown op %q", r.Op)}
	}
	return nil
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// ParseRequestOld decodes one request-pipe line. A line that is not a JSON object fails
// with [ErrMalformed]; a request that decodes but fails [RequestOld.Validate] is
// returned with that error, so the SDK can still answer its id.
func ParseRequestOld(line []byte) (RequestOld, error) {
	var r RequestOld
	if err := json.Unmarshal(line, &r); err != nil {
		return RequestOld{}, fmt.Errorf("%w: request: %w", ErrMalformed, err)
	}
	return r, r.Validate()
}

// Outcome is how a node or step body ended.
type Outcome string

// The outcomes a run_node or run_step result carries.
const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailed  Outcome = "failed"
)

// RunResultOld is the result of a successful run_node or run_step reply. A body
// that returns an error is still a successful reply, with outcome failed and
// Error set; a failed reply means the SDK could not run the body at all.
// Output is the body's JSON output; of a node's steps only the result step
// carries it.
type RunResultOld struct {
	Outcome Outcome         `json:"outcome"`
	Error   string          `json:"error,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
}

// Validate checks the outcome and that the output fits in [MaxOutputBytes].
func (r RunResultOld) Validate() error {
	if r.Outcome != OutcomeSuccess && r.Outcome != OutcomeFailed {
		return fmt.Errorf("%w: unknown outcome %q", ErrMalformed, r.Outcome)
	}
	return CheckOutputSize(len(r.Output))
}

// CheckOutputSize refuses a step output of n bytes of JSON above
// [MaxOutputBytes].
func CheckOutputSize(n int) error {
	if n > MaxOutputBytes {
		return fmt.Errorf("step output is %d bytes as JSON, over the %d MiB limit", n, MaxOutputBytes>>20)
	}
	return nil
}

// EvalResultOld is the result of a successful eval reply. Value's type depends
// on the closure's kind: a boolean for skip_if, a string or null for
// cache_key, null for before_run, verify and after_run, and an array of node
// documents for a generator. Error is non-nil when the closure returned an
// error or threw, which is still a successful reply; it is a pointer so an
// error with an empty message still reads as a failure.
type EvalResultOld struct {
	Value json.RawMessage `json:"value"`
	Error *string         `json:"error,omitempty"`
}
