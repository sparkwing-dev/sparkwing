//go:build unix

package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	nodeProtocolEntryEnv = "SW_TEST_NODE_PROTOCOL_ENTRY"
	servePipeline        = "node-protocol-serve-fixture"
	serveEntrypoint      = "NodeProtocolServeEntry"
	forgedReply          = `{"reply":"5","ok":true,"result":{"forged":true}}`
)

type serveFixtureInputs struct {
	Env   string `flag:"env" required:"true"`
	Count int    `flag:"count" default:"3"`
	Panic bool   `flag:"panic"`
}

type serveDeployArgs struct {
	Replicas int `flag:"replicas" default:"1"`
}

type serveDeployJob struct {
	sparkwing.Base
	sparkwing.WithArgs[serveDeployArgs]
}

func (*serveDeployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(w, "apply", func(context.Context) error { return nil }), nil
}

type serveFixturePipeline struct{ sparkwing.Base }

func (serveFixturePipeline) Plan(ctx context.Context, plan *sparkwing.Plan, in serveFixtureInputs, rc sparkwing.RunContext) error {
	fmt.Println(forgedReply)
	if in.Panic {
		panic("plan blew up")
	}
	branch := ""
	if rc.Git != nil {
		branch = rc.Git.Branch
	}
	sparkwing.Info(ctx, "planning %s for %s", in.Env, branch)
	if initHeld != "" {
		return fmt.Errorf("a subprocess started during package init inherited protocol descriptors %q", initHeld)
	}
	if held := heldProtocolFDs(); held != "" {
		return fmt.Errorf("a subprocess inherited protocol descriptors %q", held)
	}
	build := sparkwing.Job(plan, "build", protocolStepsJob{})
	sparkwing.Job(plan, "deploy", &serveDeployJob{}).Needs(build)
	return nil
}

func heldProtocolFDs() string {
	held, err := exec.Command("sh", "-c",
		`(: <&3) 2>/dev/null && echo 3; (: >&4) 2>/dev/null && echo 4; true`).Output()
	if err != nil {
		return err.Error()
	}
	return string(held)
}

var initHeld string

// safety: the test binary cannot take --sw-node-protocol past its own flag
// parsing, so the entry child swaps it for the run pattern here, after
// protofd saw it, and the test puts it back. The subprocess started here is
// the one a package that imports the SDK can start during init.
func init() {
	if os.Getenv(nodeProtocolEntryEnv) != "1" || len(os.Args) != 2 || os.Args[1] != protofd.Arg {
		return
	}
	initHeld = heldProtocolFDs()
	os.Args = []string{os.Args[0], "-test.run=^TestNodeProtocolEntryProcess$"}
}

// safety: the real pipeline entry, run as a child of the test binary, so the
// descriptors and stdout are the ones a host hands a pipeline binary.
func TestNodeProtocolEntryProcess(t *testing.T) {
	if os.Getenv(nodeProtocolEntryEnv) != "1" {
		return
	}
	sparkwing.Register[serveFixtureInputs](servePipeline, func() sparkwing.Pipeline[serveFixtureInputs] {
		return serveFixturePipeline{}
	})
	sparkwing.RegisterEntrypoint[serveFixtureInputs](serveEntrypoint, func() sparkwing.Pipeline[serveFixtureInputs] {
		return serveFixturePipeline{}
	})
	os.Args = []string{os.Args[0], protofd.Arg}
	sparkwing.Main()
}

type entrySession struct {
	t      *testing.T
	cmd    *exec.Cmd
	req    *os.File
	frames *nodeproto.Reader
	stdout bytes.Buffer
	nextID int
	proto  protocolSchema
	doc    protocolSchema
}

func startEntry(t *testing.T, job ...string) *entrySession {
	t.Helper()
	reqR, reqW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	frR, frW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &entrySession{
		t: t, req: reqW, frames: nodeproto.NewReader(frR),
		proto: loadProtocolSchema(t, "v2-wire.schema.json"),
		doc:   loadProtocolSchema(t, "v2-documents.schema.json"),
	}
	s.cmd = exec.Command(os.Args[0], protofd.Arg)
	s.cmd.Dir = t.TempDir()
	s.cmd.Env = append(os.Environ(), nodeProtocolEntryEnv+"=1")
	s.cmd.ExtraFiles = []*os.File{reqR, frW}
	s.cmd.Stdout = &s.stdout
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = reqR.Close()
	_ = frW.Close()
	t.Cleanup(func() {
		_ = reqW.Close()
		_ = frR.Close()
		if s.cmd.ProcessState == nil {
			_ = s.cmd.Process.Kill()
			_ = s.cmd.Wait()
		}
	})
	hello := nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &nodeproto.RunnerIdentity{Name: "fixture", Type: "local", Labels: []string{}}}
	if len(job) > 0 {
		hello.Binding = &nodeproto.ProcessBinding{RunID: "run-1", JobID: job[0], Routes: nodeproto.ProcessRoutes{URL: "http://127.0.0.1:1", Bearer: "fixture"}}
	}
	reply, _ := s.call(hello)
	if !reply.OK {
		t.Fatalf("hello: %+v", reply)
	}
	return s
}

// safety: returns the log records that arrived before the reply, so a test
// can see plan code's logging went to the frame pipe.
func (s *entrySession) call(req nodeproto.Request) (nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	id := s.send(req)
	reply, logs := s.next()
	if reply.ID != id {
		s.t.Fatalf("reply to %s while %s is the only request in flight", reply.ID, id)
	}
	return reply, logs
}

// safety: closing the request pipe is how the host ends a process, which must
// then exit 0 without another frame.
func (s *entrySession) end() {
	s.t.Helper()
	if err := s.req.Close(); err != nil {
		s.t.Fatal(err)
	}
	if line, err := s.frames.ReadLine(); !errors.Is(err, io.EOF) {
		s.t.Errorf("frame after end of input: %q, %v", line, err)
	}
	if err := s.cmd.Wait(); err != nil {
		s.t.Errorf("entry exit after end of input: %v", err)
	}
}

func wantCode(t *testing.T, r nodeproto.Reply, code apierr.Code) {
	t.Helper()
	if r.OK || r.Error == nil || r.Error.Code != code {
		t.Errorf("reply %+v, want a failure with code %s", r, code)
	}
}

func planRequest(args map[string]string) nodeproto.Request {
	return planRequestFor(servePipeline, args)
}

func planRequestFor(pipeline string, args map[string]string) nodeproto.Request {
	return nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: args, Run: &nodeproto.RunContext{
		RunID: "run-1", Pipeline: pipeline, Trigger: &nodeproto.Trigger{Source: "push"},
		Git: &nodeproto.Git{SHA: "4f2c1e0", Branch: "main", Repo: "acme/app"},
	}}
}

func TestNodeProtocolEntryServesDescribeAndPlan(t *testing.T) {
	s := startEntry(t)

	reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpDescribe})
	var described nodeproto.DescribeDoc
	if err := reply.Decode(&described); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(described.Pipelines, func(p nodeproto.PipelineDescription) bool {
		return p.Name == servePipeline
	}) {
		t.Errorf("describe = %+v, want listing %s", described, servePipeline)
	}

	reply, logs := s.call(planRequest(map[string]string{"env": "prod"}))
	var doc nodeproto.PlanDoc
	if err := reply.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Jobs) != 2 {
		t.Errorf("plan = %+v, want two jobs", doc)
	}
	if len(logs) != 1 || logs[0]["msg"] != "planning prod for main" {
		t.Errorf("plan logged %v on the frame pipe, want the plan's one Info record", logs)
	}

	reply, _ = s.call(planRequest(map[string]string{"env": "prod"}))
	wantCode(t, reply, apierr.ProtocolError)

	s.end()
	if !strings.Contains(s.stdout.String(), forgedReply) {
		t.Errorf("stdout = %q, want the plan's own print of %s", s.stdout.String(), forgedReply)
	}
}

func TestNodeProtocolEntryRefusesBadPlans(t *testing.T) {
	for name, tc := range map[string]struct {
		req  nodeproto.Request
		want apierr.Code
	}{
		"unknown pipeline":   {nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "nope", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "r", Pipeline: "nope"}}, apierr.UnknownPipeline},
		"missing flag":       {planRequest(map[string]string{}), apierr.InvalidArgs},
		"bad int":            {planRequest(map[string]string{"env": "prod", "count": "many"}), apierr.InvalidArgs},
		"bad job flag":       {planRequest(map[string]string{"env": "prod", "replicas": "many"}), apierr.InvalidArgs},
		"bad entrypoint int": {planRequestFor(serveEntrypoint, map[string]string{"env": "prod", "count": "many"}), apierr.InvalidArgs},
	} {
		t.Run(name, func(t *testing.T) {
			s := startEntry(t)
			reply, _ := s.call(tc.req)
			wantCode(t, reply, tc.want)
			s.end()
		})
	}
}

func TestNodeProtocolEntrySurvivesAPanickingPlan(t *testing.T) {
	s := startEntry(t)
	reply, _ := s.call(planRequest(map[string]string{"env": "prod", "panic": "true"}))
	wantCode(t, reply, apierr.PlanFailed)
	if reply.Error != nil && !strings.Contains(reply.Error.Message, "panicked") {
		t.Errorf("panic reply %q does not carry the panic", reply.Error.Message)
	}
	if reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpDescribe}); !reply.OK {
		t.Errorf("describe after the panic = %+v, want an answer from the same process", reply)
	}
	s.end()
}

type protocolStepsJob struct{}

func (protocolStepsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	build := sparkwing.Step(w, "build", func(context.Context) error { return nil }).
		SkipIf(func(context.Context) bool { return false })
	sparkwing.Step(w, "test", func(context.Context) error { return nil }).Needs(build).Optional()
	return nil, nil
}
