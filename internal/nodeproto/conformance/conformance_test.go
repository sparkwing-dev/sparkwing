//go:build unix

package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

type corpus struct {
	Sessions []corpusSession `json:"sessions"`
}

type corpusSession struct {
	Name     string       `json:"name"`
	Binding  bool         `json:"binding"`
	Protocol int          `json:"protocol,omitempty"`
	Requests []corpusStep `json:"requests"`
}

type corpusStep struct {
	Op       nodeproto.Op `json:"op"`
	ID       string       `json:"id,omitempty"`
	Kind     string       `json:"kind,omitempty"`
	Code     string       `json:"code,omitempty"`
	Contains string       `json:"contains,omitempty"`
	Name     string       `json:"name,omitempty"`
}

func TestGoSDKConformsV2(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gofixture")
	build := exec.Command("go", "build", "-o", bin, "./gofixture")
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
				}
				if step.Op == nodeproto.OpEval {
					p.validate(t, p.wire, "EvalResult", got.Result)
				}
				if step.Contains != "" && !bytes.Contains(got.Result, []byte(step.Contains)) {
					t.Fatalf("%s result %s lacks %q", step.Op, got.Result, step.Contains)
				}
			}
			p.end(t)
		})
	}
}

type fixtureProcess struct {
	command   *exec.Cmd
	request   *os.File
	frames    *nodeproto.Reader
	frameR    *os.File
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	plan      nodeproto.PlanDoc
	wire      *Schema
	documents *Schema
}

func startFixture(t *testing.T, bin string) *fixtureProcess {
	t.Helper()
	requestR, requestW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	frameR, frameW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &fixtureProcess{request: requestW, frames: nodeproto.NewReader(frameR), frameR: frameR}
	p.wire, err = LoadSchema(filepath.Join("testdata", "v2-wire.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.documents, err = LoadSchema(filepath.Join("testdata", "v2-documents.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.command = exec.CommandContext(t.Context(), bin, protofd.Arg)
	p.command.Dir = t.TempDir()
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "SPARKWING_") {
			p.command.Env = append(p.command.Env, item)
		}
	}
	p.command.ExtraFiles = []*os.File{requestR, frameW}
	p.command.Stdout, p.command.Stderr = &p.stdout, &p.stderr
	if err := p.command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = requestR.Close()
	_ = frameW.Close()
	t.Cleanup(func() {
		_ = requestW.Close()
		_ = frameR.Close()
		if p.command.ProcessState == nil {
			_ = p.command.Process.Kill()
			_ = p.command.Wait()
		}
	})
	return p
}

func (p *fixtureProcess) send(t *testing.T, req nodeproto.Request) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.request.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

func (p *fixtureProcess) reply(t *testing.T) nodeproto.Reply {
	t.Helper()
	for {
		line, err := p.frames.ReadLine()
		if err != nil {
			t.Fatalf("frame read: %v; stderr: %s", err, p.stderr.String())
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
	if err := p.request.Close(); err != nil {
		t.Fatal(err)
	}
	if line, err := p.frames.ReadLine(); err != io.EOF {
		t.Errorf("frame after end of requests: %q, %v", line, err)
	}
	if err := p.command.Wait(); err != nil {
		t.Fatalf("fixture exited: %v; stderr: %s", err, p.stderr.String())
	}
}
