package nativehost_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type corpusStep struct {
	Op         nodeproto.Op `json:"op"`
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	Code       string       `json:"code"`
	Contains   string       `json:"contains"`
	Name       string       `json:"name"`
	PlanVector string       `json:"plan_vector"`
	Job        string       `json:"job"`
}

type corpusSession struct {
	Name       string       `json:"name"`
	Binding    bool         `json:"binding"`
	BindingJob string       `json:"binding_job"`
	Protocol   int          `json:"protocol"`
	Requests   []corpusStep `json:"requests"`
}

type sharedCorpus struct {
	Sessions []corpusSession `json:"sessions"`
	Plans    []struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	} `json:"plans"`
}

func TestNativeConformance(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), node, filepath.Join(root, "node_modules/typescript/bin/tsc"), "-p", filepath.Join(root, "tsconfig.build.json"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build SDK: %v\n%s", err, output)
	}
	corpusDir := filepath.Join(root, "../../internal/nodeproto/conformance/testdata")
	raw, err := os.ReadFile(filepath.Join(corpusDir, "v2-corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus sharedCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*conformance.Schema{}
	for _, name := range []string{"wire", "documents"} {
		schema, err := conformance.LoadSchema(filepath.Join(corpusDir, "v2-"+name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	for _, session := range corpus.Sessions {
		t.Run(session.Name, func(t *testing.T) {
			h := startSDKAt(t, node, root, "conformance.mjs")
			protocol := session.Protocol
			if protocol == 0 {
				protocol = nodeproto.Version
			}
			hello := nodeproto.Request{ID: "hello", Op: nodeproto.OpHello, Protocol: protocol, Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}
			if session.Binding {
				job := session.BindingJob
				if job == "" {
					job = "task"
				}
				hello.Binding = &nodeproto.ProcessBinding{RunID: "r", JobID: job, Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "secret"}}
			}
			h.send(hello)
			got := nextReply(t, h)
			if protocol != nodeproto.Version {
				if got.Error == nil || got.Error.Code != "protocol_mismatch" {
					t.Fatalf("mismatch: %+v", got)
				}
			} else if !got.OK {
				t.Fatalf("hello: %+v", got)
			}
			var plan nodeproto.PlanDoc
			var generated []nodeproto.JobSpec
			for index, step := range session.Requests {
				req := corpusRequest(plan, step, index)
				h.send(req)
				reply := nextReply(t, h)
				if reply.ID != req.ID {
					t.Fatalf("reply %q, request %q", reply.ID, req.ID)
				}
				if step.Code != "" {
					if reply.OK || reply.Error == nil || string(reply.Error.Code) != step.Code {
						t.Fatalf("%s: %+v, want %s", req.Op, reply, step.Code)
					}
					continue
				}
				if !reply.OK {
					t.Fatalf("%s: %+v", req.Op, reply)
				}
				if step.Contains != "" && !bytes.Contains(reply.Result, []byte(step.Contains)) {
					t.Fatalf("%s lacks %q", reply.Result, step.Contains)
				}
				definition, schema := "", "wire"
				switch step.Op {
				case nodeproto.OpPlan:
					definition, schema = "PlanDoc", "documents"
					if _, err := nodeproto.ValidatePlan(reply.Result, nodeproto.ValidationContext{Secrets: fixtureSecrets{}, Placement: nodeproto.PlacementLocal}); err != nil {
						t.Fatal(err)
					}
					if err := reply.Decode(&plan); err != nil {
						t.Fatal(err)
					}
				case nodeproto.OpDescribe:
					definition, schema = "DescribeDoc", "documents"
				case nodeproto.OpRunStep:
					definition = "RunResult"
				case nodeproto.OpEval:
					definition = "EvalResult"
					var result nodeproto.EvalResult
					if err := reply.Decode(&result); err != nil {
						t.Fatal(err)
					}
					kind := map[string]string{"skip": "skip_if", "memo": "memo_key", "before": "before_run", "after": "after_run", "verify": "verify", "generate": "generate"}[step.Kind]
					if err := result.ValidateValue(kind); err != nil {
						t.Fatal(err)
					}
					if step.PlanVector != "" {
						generated = checkGenerated(t, corpus, plan, generated, result, step.PlanVector)
					}
				}
				if definition != "" {
					validateSchema(t, schemas[schema], definition, reply.Result)
				}
			}
			h.finish()
		})
	}
}

func nextReply(t *testing.T, h *host) nodeproto.Reply {
	t.Helper()
	for {
		frame := h.frame()
		if frame.ID != "" {
			return frame.Reply
		}
	}
}

func validateSchema(t *testing.T, schema *conformance.Schema, name string, raw []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	def, ok := schema.Def(name)
	if !ok {
		t.Fatalf("missing definition %s", name)
	}
	if failures := schema.Validate(def, value, name); len(failures) > 0 {
		t.Fatalf("%s: %v", name, failures)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type fixtureSecrets struct{}

func (fixtureSecrets) ContainsSecret(string) bool { return false }

func corpusRequest(plan nodeproto.PlanDoc, step corpusStep, index int) nodeproto.Request {
	req := nodeproto.Request{ID: fmt.Sprintf("%d", index+1), Op: step.Op}
	if step.ID != "" {
		req.ID = step.ID
	}
	switch step.Op {
	case nodeproto.OpPlan:
		name := step.Name
		if name == "" {
			name = "demo"
		}
		req.Pipeline, req.Args, req.Run = "conformance", map[string]string{"name": name}, &nodeproto.RunContext{RunID: "r", Pipeline: "conformance"}
	case nodeproto.OpRunStep:
		req.Job, req.Step, req.Attempt, req.Settled = "task", "run", 1, []string{}
		if step.Job != "" {
			req.Job = step.Job
		}
	case nodeproto.OpEval:
		closures := plan.Jobs[0].Closures
		switch step.Kind {
		case "skip":
			req.Closure = closures.SkipIf[0]
		case "memo":
			req.Closure = closures.MemoKey
		case "before":
			req.Closure = closures.BeforeRun[0]
		case "verify":
			req.Closure = closures.Verify
		case "after":
			req.Closure, req.Input = closures.AfterRun[0], json.RawMessage(`{"outcome":"success"}`)
		case "generate":
			req.Closure = closures.Generate[0]
		}
	case nodeproto.OpCancel:
		req.Job, req.Step = "task", "run"
	}
	return req
}

func checkGenerated(t *testing.T, corpus sharedCorpus, plan nodeproto.PlanDoc, generated []nodeproto.JobSpec, result nodeproto.EvalResult, vectorName string) []nodeproto.JobSpec {
	t.Helper()
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	raw := append(append([]byte(`{"jobs":`), result.Value...), '}')
	if _, err := nodeproto.ValidatePlan(raw, nodeproto.ValidationContext{Secrets: fixtureSecrets{}, Placement: nodeproto.PlacementLocal, Expansion: true, SourceJob: "task", ExistingJobs: plan.Jobs}); err != nil {
		t.Fatal(err)
	}
	hash, err := runspec.CanonicalHash(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, vector := range corpus.Plans {
		if vector.Name == vectorName {
			found = true
			if string(hash) != vector.SHA256 {
				t.Fatalf("generated hash %s, want %s: %s", hash, vector.SHA256, raw)
			}
		}
	}
	if !found {
		t.Fatalf("missing vector %s", vectorName)
	}
	var jobs []nodeproto.JobSpec
	if err := json.Unmarshal(result.Value, &jobs); err != nil {
		t.Fatal(err)
	}
	if generated != nil && !bytes.Equal(mustJSON(t, generated), mustJSON(t, jobs)) {
		t.Fatal("generated replay drifted")
	}
	return jobs
}
