package nodeproto

import (
	"encoding/json"
	"time"
)

// safety: Version is checked once in hello; documents and HTTP headers carry no protocol version.
const Version = 2

// safety: Op keeps the complete launch operation vocabulary separate from optional hello features.
type Op string

// safety: No run_node operation remains; body-only jobs declare one step.
const (
	OpHello    Op = "hello"
	OpDescribe Op = "describe"
	OpPlan     Op = "plan"
	OpRunStep  Op = "run_step"
	OpEval     Op = "eval"
	OpCancel   Op = "cancel"
)

// safety: Request binds execution by job and step while keeping credentials only in hello memory.
type Request struct {
	ID       string            `json:"id"`
	Op       Op                `json:"op"`
	Protocol int               `json:"protocol,omitempty"`
	Engine   *EngineIdentity   `json:"engine,omitempty"`
	Runner   *RunnerIdentity   `json:"runner,omitempty"`
	Binding  *ProcessBinding   `json:"binding,omitempty"`
	Pipeline string            `json:"pipeline,omitempty"`
	Args     map[string]string `json:"args,omitzero"`
	Run      *RunContext       `json:"run,omitempty"`
	Job      string            `json:"job,omitempty"`
	Step     string            `json:"step,omitempty"`
	Attempt  int               `json:"attempt,omitempty"`
	Failure  *JobFailure       `json:"failure,omitempty"`
	Closure  string            `json:"closure,omitempty"`
	Input    json.RawMessage   `json:"input,omitempty"`
}

// safety: EngineIdentity permits actionable mismatch diagnostics without stamping every request.
type EngineIdentity struct {
	Version string `json:"version"`
}

// safety: RunnerIdentity comes from the granted claim, never self-reported SDK environment.
type RunnerIdentity struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Labels []string `json:"labels"`
}

// safety: ProcessBinding is absent during describe and plan, where no job routes may be used.
type ProcessBinding struct {
	RunID  string        `json:"run_id"`
	JobID  string        `json:"job_id"`
	Routes ProcessRoutes `json:"routes"`
}

// safety: ProcessRoutes holds only the delegated node-host bearer, never a controller credential.
type ProcessRoutes struct {
	URL    string `json:"url"`
	Bearer string `json:"bearer"`
}

// safety: HelloResult advertises optional operations so additive protocol changes need no major bump.
type HelloResult struct {
	Protocol int         `json:"protocol"`
	SDK      SDKIdentity `json:"sdk"`
	Features []string    `json:"features"`
}

// safety: SDKIdentity lets a mismatch name the package that must be rebuilt or upgraded.
type SDKIdentity struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

// safety: RunContext is a sealed host snapshot; SDKs never query the controller for these facts.
type RunContext struct {
	RunID     string    `json:"run_id"`
	Pipeline  string    `json:"pipeline"`
	Git       *Git      `json:"git,omitempty"`
	Trigger   *Trigger  `json:"trigger,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	NoMemo    bool      `json:"no_memo,omitempty"`
	DryRun    bool      `json:"dry_run,omitempty"`
}

// safety: JobFailure gives recovery code the source failure without inventing a second error envelope.
type JobFailure struct {
	Job   string          `json:"job"`
	Stage string          `json:"stage"`
	Error json.RawMessage `json:"error"`
}

// safety: Reply.Error carries apierr's inner JSON object without another wire schema.
// Exactly one of Result or Error is present; error codes have no registry in this package.
type Reply struct {
	ID     string          `json:"reply"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

// safety: RunResult separates body failure from an SDK failure to execute the request.
type RunResult struct {
	Outcome string          `json:"outcome"`
	Error   json.RawMessage `json:"error,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
}

// safety: EvalResult can carry scalar closure values or generated jobs without changing framing.
type EvalResult struct {
	Value json.RawMessage `json:"value"`
	Error json.RawMessage `json:"error,omitempty"`
}

// safety: FrameLog has no job field because the host, not pipeline code, assigns log attribution.
type FrameLog struct {
	TS      string          `json:"ts"`
	Level   string          `json:"level,omitempty"`
	Step    string          `json:"step,omitempty"`
	Event   string          `json:"event,omitempty"`
	Message string          `json:"msg,omitempty"`
	Attrs   json.RawMessage `json:"attrs,omitempty"`
}

// safety: Frame represents either a reply or log record on the isolated frame pipe, never stdout.
type Frame struct {
	Reply *Reply
	Log   *FrameLog
}

// safety: ParseRequest ignores additive fields while enforcing each operation's required fields.
func ParseRequest(line []byte) (Request, error) { panic("unimplemented: c3-protocol-sdk") }

// safety: ParseFrame rejects ambiguous or malformed frames; unknown log events remain readable.
func ParseFrame(line []byte) (Frame, error) { panic("unimplemented: c3-protocol-sdk") }

// safety: Validate refuses fields that contradict the selected operation before invoking pipeline code.
func (r Request) Validate() error { panic("unimplemented: c3-protocol-sdk") }

// safety: Decode uses C2 apierr codes for failed replies rather than matching error messages.
func (r Reply) Decode(result any) error { panic("unimplemented: c3-protocol-sdk") }
