package nodeproto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
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
	// Target identifies the active request cancelled by OpCancel, including eval without job/step.
	Target string `json:"target,omitempty"`
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
	Job   string        `json:"job"`
	Stage string        `json:"stage"`
	Error *apierr.Error `json:"error"`
}

// safety: Reply.Error is decoded by apierr.DecodeInner, without a second envelope.
// Exactly one of Result or Error is present; error codes have no registry in this package.
type Reply struct {
	ID     string          `json:"reply"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *apierr.Error   `json:"error,omitempty"`
}

// safety: RunResult separates body failure from an SDK failure to execute the request.
type RunResult struct {
	Outcome string          `json:"outcome"`
	Error   *apierr.Error   `json:"error,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
}

// safety: EvalResult can carry scalar closure values or generated jobs without changing framing.
type EvalResult struct {
	Value json.RawMessage `json:"value"`
	Error *apierr.Error   `json:"error,omitempty"`
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

// safety: ParseRequest ignores additive fields, checks operation fields and decodes failure.error with apierr.DecodeInner.
func ParseRequest(line []byte) (Request, error) {
	obj, err := protocolObject(line)
	if err != nil {
		return Request{}, err
	}
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return Request{}, fmt.Errorf("%w: request: %v", ErrMalformed, err)
	}
	if err := checkProtocolValue(line, reflect.TypeFor[Request]()); err != nil {
		return r, err
	}
	if err := requestFields(obj, r.Op); err != nil {
		return r, err
	}
	if raw, ok := obj["failure"]; ok {
		failure, err := protocolObject(raw)
		if err != nil {
			return r, err
		}
		if _, err := apierr.DecodeInner(failure["error"]); err != nil {
			return r, fmt.Errorf("%w: failure.error: %v", ErrMalformed, err)
		}
	}
	return r, r.Validate()
}

// safety: ParseFrame rejects ambiguous frames and uses apierr.DecodeInner for reply errors; unknown log events remain readable.
func ParseFrame(line []byte) (Frame, error) {
	obj, err := protocolObject(line)
	if err != nil {
		return Frame{}, err
	}
	if _, reply := obj["reply"]; reply {
		if _, log := obj["ts"]; log {
			return Frame{}, fmt.Errorf("%w: frame has reply and log fields", ErrMalformed)
		}
		if err := requiredString(obj, "reply"); err != nil {
			return Frame{}, err
		}
		var ok bool
		if raw := obj["ok"]; len(raw) == 0 || json.Unmarshal(raw, &ok) != nil || string(raw) == "null" {
			return Frame{}, fmt.Errorf("%w: reply.ok must be a boolean", ErrMalformed)
		}
		result, hasResult := obj["result"]
		errorRaw, hasError := obj["error"]
		if ok != hasResult || ok == hasError || hasResult && (string(result) == "null" || !isObject(result)) {
			return Frame{}, fmt.Errorf("%w: reply must contain exactly one result or error matching ok", ErrMalformed)
		}
		r := &Reply{OK: ok, Result: result}
		if err := json.Unmarshal(obj["reply"], &r.ID); err != nil {
			return Frame{}, fmt.Errorf("%w: reply id: %v", ErrMalformed, err)
		}
		if hasError {
			r.Error, err = apierr.DecodeInner(errorRaw)
			if err != nil {
				return Frame{}, fmt.Errorf("%w: reply.error: %v", ErrMalformed, err)
			}
		}
		return Frame{Reply: r}, nil
	}
	if err := requiredString(obj, "ts"); err != nil {
		return Frame{}, err
	}
	if _, oldJob := obj["job"]; oldJob {
		return Frame{}, fmt.Errorf("%w: log assigns a job", ErrMalformed)
	}
	for _, key := range []string{"level", "step", "event", "msg"} {
		if raw, found := obj[key]; found && (string(raw) == "null" || json.Unmarshal(raw, new(string)) != nil) {
			return Frame{}, fmt.Errorf("%w: log.%s must be a string", ErrMalformed, key)
		}
	}
	if raw, found := obj["attrs"]; found && !isObject(raw) {
		return Frame{}, fmt.Errorf("%w: log.attrs must be an object", ErrMalformed)
	}
	var log FrameLog
	if err := checkProtocolValue(line, reflect.TypeFor[FrameLog]()); err != nil {
		return Frame{}, err
	}
	if err := json.Unmarshal(line, &log); err != nil {
		return Frame{}, fmt.Errorf("%w: log: %v", ErrMalformed, err)
	}
	return Frame{Log: &log}, nil
}

// safety: Validate refuses fields that contradict the selected operation before invoking pipeline code.
func (r Request) Validate() error {
	bad := func(message string) error { return &apierr.Error{Code: apierr.BadRequest, Message: message} }
	if r.ID == "" {
		return bad("request needs a correlation id")
	}
	switch r.Op {
	case OpHello:
		if r.Protocol != Version {
			return &apierr.Error{Code: apierr.ProtocolMismatch, Message: "node protocol major does not match", Details: map[string]any{"supported_majors": []int{Version}}}
		}
		if r.Engine == nil || r.Engine.Version == "" || r.Runner == nil || r.Runner.Name == "" || r.Runner.Type == "" || r.Runner.Labels == nil {
			return bad("hello needs engine and granted runner identity")
		}
		if b := r.Binding; b != nil && (b.RunID == "" || b.JobID == "" || b.Routes.URL == "" || b.Routes.Bearer == "") {
			return bad("hello binding needs a run, job and node routes")
		}
	case OpDescribe:
	case OpPlan:
		if r.Pipeline == "" || r.Args == nil || r.Run == nil || r.Run.RunID == "" || r.Run.Pipeline == "" {
			return bad("plan needs an SDK entrypoint, args object and invoked pipeline in run context")
		}
	case OpRunStep:
		if r.Job == "" || r.Step == "" || r.Attempt < 1 {
			return bad("run_step needs job, step and positive attempt")
		}
	case OpEval:
		if r.Closure == "" {
			return bad("eval needs a closure")
		}
	case OpCancel:
		if r.Target == "" && r.Job == "" || r.Target != "" && (r.Job != "" || r.Step != "") {
			return bad("cancel needs target alone, or job with optional step")
		}
	default:
		return &apierr.Error{Code: apierr.UnknownOp, Message: fmt.Sprintf("unknown node operation %q; rebuild .sparkwing if the SDK is older than the engine", r.Op)}
	}
	if r.Op != OpHello && (r.Protocol != 0 || r.Engine != nil || r.Runner != nil || r.Binding != nil) ||
		r.Op != OpPlan && (r.Pipeline != "" || r.Args != nil || r.Run != nil) ||
		r.Op != OpRunStep && (r.Attempt != 0 || r.Failure != nil) ||
		r.Op != OpRunStep && r.Op != OpCancel && (r.Job != "" || r.Step != "") ||
		r.Op != OpCancel && r.Target != "" ||
		r.Op != OpEval && (r.Closure != "" || len(r.Input) != 0) {
		return bad("request carries fields for another operation")
	}
	if f := r.Failure; f != nil && (f.Job == "" || f.Stage == "" || f.Error == nil) {
		return bad("failure needs a job, stage and error")
	}
	if f := r.Failure; f != nil {
		raw, err := json.Marshal(f.Error)
		if err != nil {
			return bad("failure error cannot be encoded")
		}
		if _, err := apierr.DecodeInner(raw); err != nil {
			return bad("failure error is invalid")
		}
	}
	return nil
}

// safety: Decode validates reply, RunResult and EvalResult errors with apierr.DecodeInner before returning typed results.
func (r Reply) Decode(result any) error {
	if r.ID == "" || r.OK == (r.Error != nil) || r.OK != (len(r.Result) != 0) {
		return fmt.Errorf("%w: inconsistent reply", ErrMalformed)
	}
	if !r.OK {
		raw, err := json.Marshal(r.Error)
		if err != nil {
			return fmt.Errorf("%w: reply.error: %v", ErrMalformed, err)
		}
		validated, err := apierr.DecodeInner(raw)
		if err != nil {
			return fmt.Errorf("%w: reply.error: %v", ErrMalformed, err)
		}
		return validated
	}
	destination := reflect.ValueOf(result)
	if destination.Kind() != reflect.Pointer || destination.IsNil() {
		return fmt.Errorf("%w: missing result destination", ErrMalformed)
	}
	if _, err := protocolObject(r.Result); err != nil {
		return err
	}
	if err := checkProtocolValue(r.Result, reflect.TypeOf(result)); err != nil {
		return err
	}
	// safety: absent fields cannot inherit values from a previous reply.
	destination.Elem().SetZero()
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("%w: reply.result: %v", ErrMalformed, err)
	}
	switch v := result.(type) {
	case *HelloResult:
		if v.Protocol != Version || v.SDK.Language == "" || v.SDK.Version == "" || v.Features == nil {
			return fmt.Errorf("%w: invalid hello result", ErrMalformed)
		}
	case *RunResult:
		obj, err := protocolObject(r.Result)
		if err != nil {
			return err
		}
		if v.Outcome != "success" && v.Outcome != "failed" {
			return fmt.Errorf("%w: unknown run outcome", ErrMalformed)
		}
		if raw, ok := obj["error"]; ok {
			v.Error, err = apierr.DecodeInner(raw)
			if err != nil {
				return fmt.Errorf("%w: run result.error: %v", ErrMalformed, err)
			}
		}
		if v.Outcome == "failed" && v.Error == nil || v.Outcome == "success" && v.Error != nil {
			return fmt.Errorf("%w: run outcome and error disagree", ErrMalformed)
		}
		if int64(len(v.Output)) > limits.NodeOutputBytes {
			return fmt.Errorf("%w: run output exceeds the output limit", ErrMalformed)
		}
	case *EvalResult:
		obj, err := protocolObject(r.Result)
		if err != nil {
			return err
		}
		if raw, ok := obj["error"]; ok {
			v.Error, err = apierr.DecodeInner(raw)
			if err != nil {
				return fmt.Errorf("%w: eval result.error: %v", ErrMalformed, err)
			}
		}
	}
	return nil
}

func protocolObject(raw []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return nil, fmt.Errorf("%w: expected one JSON object", ErrMalformed)
	}
	obj := map[string]json.RawMessage{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: object key: %v", ErrMalformed, err)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%w: object key is not a string", ErrMalformed)
		}
		if _, exists := obj[key]; exists {
			return nil, fmt.Errorf("%w: duplicate field %s", ErrMalformed, key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: field %s: %v", ErrMalformed, key, err)
		}
		obj[key] = value
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: object end: %v", ErrMalformed, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	return obj, nil
}

func requiredString(obj map[string]json.RawMessage, key string) error {
	var value string
	if raw := obj[key]; len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value == "" {
		return fmt.Errorf("%w: %s must be a nonempty string", ErrMalformed, key)
	}
	return nil
}

func requestFields(obj map[string]json.RawMessage, op Op) error {
	allowed := map[string]bool{"id": true, "op": true}
	switch op {
	case OpHello:
		allowed["protocol"], allowed["engine"], allowed["runner"], allowed["binding"] = true, true, true, true
	case OpDescribe:
	case OpPlan:
		allowed["pipeline"], allowed["args"], allowed["run"] = true, true, true
	case OpRunStep:
		allowed["job"], allowed["step"], allowed["attempt"], allowed["failure"] = true, true, true, true
	case OpEval:
		allowed["closure"], allowed["input"] = true, true
	case OpCancel:
		allowed["job"], allowed["step"], allowed["target"] = true, true, true
	default:
		return nil
	}
	for _, key := range []string{"protocol", "engine", "runner", "binding", "pipeline", "args", "run", "job", "step", "attempt", "failure", "closure", "input", "target"} {
		if raw, found := obj[key]; found && (!allowed[key] || string(raw) == "null" && key != "input") {
			return &apierr.Error{Code: apierr.BadRequest, Message: fmt.Sprintf("operation %s cannot carry %s", op, key)}
		}
	}
	if err := requiredString(obj, "id"); err != nil {
		return err
	}
	if err := requiredString(obj, "op"); err != nil {
		return err
	}
	if op == OpCancel {
		if _, target := obj["target"]; target {
			if _, job := obj["job"]; job {
				return fmt.Errorf("%w: cancel selectors cannot be mixed", ErrMalformed)
			}
			if _, step := obj["step"]; step {
				return fmt.Errorf("%w: cancel selectors cannot be mixed", ErrMalformed)
			}
			return requiredString(obj, "target")
		}
		if err := requiredString(obj, "job"); err != nil {
			return err
		}
		if _, step := obj["step"]; step {
			return requiredString(obj, "step")
		}
	}

	if op == OpPlan {
		if raw, ok := obj["args"]; !ok || !isObject(raw) {
			return &apierr.Error{Code: apierr.BadRequest, Message: "plan args must be an object"}
		}
	}
	return nil
}
