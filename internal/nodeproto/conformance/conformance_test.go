package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type corpus struct {
	Sessions []corpusSession `json:"sessions"`
}

type corpusSession struct {
	Name       string       `json:"name"`
	Binding    bool         `json:"binding"`
	BindingJob string       `json:"binding_job,omitempty"`
	Protocol   int          `json:"protocol,omitempty"`
	Requests   []corpusStep `json:"requests"`
}

type corpusStep struct {
	Op         nodeproto.Op `json:"op"`
	ID         string       `json:"id,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Code       string       `json:"code,omitempty"`
	Contains   string       `json:"contains,omitempty"`
	Name       string       `json:"name,omitempty"`
	PlanVector string       `json:"plan_vector,omitempty"`
	Job        string       `json:"job,omitempty"`
}

func TestGoSDKConformsV2(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gofixture")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-p", "4", "-o", bin, "./gofixture")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "v2-corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases corpus
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	vectors := map[string]declarationVector{}
	for _, vector := range readVectors(t).Plans {
		vectors[vector.Name] = vector
	}
	for _, session := range cases.Sessions {
		t.Run(session.Name, func(t *testing.T) {
			p := startFixture(t, bin)
			protocol := nodeproto.Version
			if session.Protocol != 0 {
				protocol = session.Protocol
			}
			hello := nodeproto.Request{
				ID: "hello", Op: nodeproto.OpHello, Protocol: protocol,
				Engine: &nodeproto.EngineIdentity{Version: "fixture"},
				Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}},
			}
			if session.Binding {
				hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: "task", Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "secret"}}
				if session.BindingJob != "" {
					hello.Binding.JobID = session.BindingJob
				}
			}
			p.send(t, hello)
			frame := p.reply(t)
			if protocol == nodeproto.Version && (!frame.OK || frame.Result == nil) {
				t.Fatalf("hello: %+v", frame)
			}
			if protocol != nodeproto.Version && (frame.Error == nil || frame.Error.Code != apierr.ProtocolMismatch) {
				t.Fatalf("mismatch: %+v", frame)
			}
			for i, step := range session.Requests {
				request := p.makeRequest(step, i)
				p.send(t, request)
				got := p.reply(t)
				if got.ID != request.ID {
					t.Fatalf("reply ID %q, request ID %q", got.ID, request.ID)
				}
				if step.Code != "" {
					if got.OK || got.Error == nil || string(got.Error.Code) != step.Code {
						t.Fatalf("%s: %+v, want code %s", step.Op, got, step.Code)
					}
					continue
				}
				if !got.OK {
					t.Fatalf("%s: %+v", step.Op, got)
				}
				if step.Op == nodeproto.OpPlan {
					p.validate(t, p.documents, "PlanDoc", got.Result)
					if bytes.Contains(got.Result, []byte(`"protocol"`)) || bytes.Contains(got.Result, []byte(`"spec_hash"`)) {
						t.Fatalf("plan carries host fields: %s", got.Result)
					}
					var plan nodeproto.PlanDoc
					if err := got.Decode(&plan); err != nil || len(plan.Jobs) != 1 || plan.Jobs[0].ID != "task" {
						t.Fatalf("plan: %+v, %v", plan, err)
					}
					p.plan = plan
				}
				if step.Op == nodeproto.OpDescribe {
					p.validate(t, p.documents, "DescribeDoc", got.Result)
				}
				if step.Op == nodeproto.OpRunStep {
					p.validate(t, p.wire, "RunResult", got.Result)
					var result nodeproto.RunResult
					if err := got.Decode(&result); err != nil {
						t.Fatal(err)
					}
				}
				if step.Op == nodeproto.OpEval {
					p.validate(t, p.wire, "EvalResult", got.Result)
					p.checkEval(t, got, step, vectors)
				}
				if step.Contains != "" && !bytes.Contains(got.Result, []byte(step.Contains)) {
					t.Fatalf("%s result %s lacks %q", step.Op, got.Result, step.Contains)
				}
			}
			p.end(t)
		})
	}
}

func (p *fixtureProcess) checkEval(t *testing.T, reply nodeproto.Reply, step corpusStep, vectors map[string]declarationVector) {
	t.Helper()
	var result nodeproto.EvalResult
	if err := reply.Decode(&result); err != nil {
		t.Fatal(err)
	}
	kind := map[string]string{"skip": "skip_if", "memo": "memo_key", "before": "before_run", "after": "after_run", "verify": "verify", "generate": "generate"}[step.Kind]
	if err := result.ValidateValue(kind); err != nil {
		t.Fatal(err)
	}
	if step.PlanVector == "" {
		return
	}
	vector, ok := vectors[step.PlanVector]
	if !ok || step.Kind != "generate" || vector.Code != "" || vector.SourceJob == "" {
		t.Fatalf("invalid expansion vector %q", step.PlanVector)
	}
	if result.Error != nil {
		t.Fatalf("expansion failed: %v", result.Error)
	}
	raw := append(append([]byte(`{"jobs":`), result.Value...), '}')
	if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{
		Placement: nodeproto.PlacementLocal, Secrets: vectorSecrets{}, Expansion: true,
		SourceJob: vector.SourceJob, ExistingJobs: p.plan.Jobs, ExistingConcurrency: p.plan.Concurrency,
	}); err != nil {
		t.Fatal(err)
	}
	hash, err := runspec.CanonicalHash(json.RawMessage(raw))
	if err != nil || string(hash) != vector.SHA256 {
		t.Fatalf("generated declaration = %s, hash = %s, %v; want %s", raw, hash, err, vector.Canonical)
	}
}

type fixtureProcess struct {
	process   *acceptance.GoSDKProcess
	plan      nodeproto.PlanDoc
	wire      *Schema
	documents *Schema
}

func startFixture(t *testing.T, bin string) *fixtureProcess {
	t.Helper()
	p := &fixtureProcess{}
	var err error
	p.wire, err = LoadSchema(filepath.Join("testdata", "v2-wire.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.documents, err = LoadSchema(filepath.Join("testdata", "v2-documents.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.process, err = acceptance.StartGoSDKProcess(t.Context(), bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.process.Close)
	return p
}

func (p *fixtureProcess) send(t *testing.T, req nodeproto.Request) {
	t.Helper()
	if err := p.process.Send(req); err != nil {
		t.Fatal(err)
	}
}

func (p *fixtureProcess) reply(t *testing.T) nodeproto.Reply {
	t.Helper()
	for {
		line, err := p.process.NextFrame()
		if err != nil {
			p.process.Close()
			t.Fatalf("frame read: %v; stderr: %s", err, p.process.Stderr())
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			t.Fatal(err)
		}
		if _, isReply := fields["reply"]; !isReply {
			p.validate(t, p.wire, "FrameLog", line)
			continue
		}
		p.validate(t, p.wire, "Reply", line)
		var reply nodeproto.Reply
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		_, hasResult := fields["result"]
		_, hasError := fields["error"]
		if reply.OK != hasResult || reply.OK == hasError || (hasError && reply.Error == nil) {
			t.Fatalf("inconsistent reply envelope: %s", line)
		}
		return reply
	}
}

func (p *fixtureProcess) validate(t *testing.T, schema *Schema, definition string, raw []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	def, ok := schema.Def(definition)
	if !ok {
		t.Fatalf("missing schema definition %s", definition)
	}
	if problems := schema.Validate(def, value, definition); len(problems) > 0 {
		t.Fatalf("schema %s: %s", definition, strings.Join(problems, "; "))
	}
}

func (p *fixtureProcess) makeRequest(step corpusStep, index int) nodeproto.Request {
	id := step.ID
	if id == "" {
		id = fmt.Sprintf("%d", index+1)
	}
	r := nodeproto.Request{ID: id, Op: step.Op}
	switch step.Op {
	case nodeproto.OpPlan:
		r.Pipeline = "conformance"
		r.Args = map[string]string{"name": "demo"}
		if step.Name != "" {
			r.Args["name"] = step.Name
		}
		r.Run = &nodeproto.RunContext{RunID: "r", Pipeline: "conformance"}
	case nodeproto.OpRunStep:
		r.Job, r.Step, r.Attempt = "task", "run", 1
		if step.Job != "" {
			r.Job = step.Job
		}
	case nodeproto.OpEval:
		if len(p.plan.Jobs) != 0 {
			switch step.Kind {
			case "skip":
				r.Closure = p.plan.Jobs[0].Closures.SkipIf[0]
			case "memo":
				r.Closure = p.plan.Jobs[0].Closures.MemoKey
			case "generate":
				r.Closure = p.plan.Jobs[0].Closures.Generate[0]
			case "before":
				r.Closure = p.plan.Jobs[0].Closures.BeforeRun[0]
			case "verify":
				r.Closure = p.plan.Jobs[0].Closures.Verify
			case "after":
				r.Closure = p.plan.Jobs[0].Closures.AfterRun[0]
				r.Input = json.RawMessage(`{"outcome":"success"}`)
			}
		}
	case nodeproto.OpCancel:
		r.Job, r.Step = "task", "run"
	}
	return r
}

func (p *fixtureProcess) end(t *testing.T) {
	t.Helper()
	if err := p.process.Finish(); err != nil {
		t.Fatal(err)
	}
}
