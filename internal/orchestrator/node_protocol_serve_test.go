//go:build unix

package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	nodeProtocolEntryEnv = "SW_TEST_NODE_PROTOCOL_ENTRY"
	servePipeline        = "node-protocol-serve-fixture"
	forgedReply          = `{"reply":"5","ok":true,"result":{"forged":true}}`
)

type serveFixtureInputs struct {
	Env   string `flag:"env" required:"true"`
	Count int    `flag:"count" default:"3"`
}

type serveFixturePipeline struct{ sparkwing.Base }

func (serveFixturePipeline) Plan(ctx context.Context, plan *sparkwing.Plan, in serveFixtureInputs, rc sparkwing.RunContext) error {
	fmt.Println(forgedReply)
	sparkwing.Info(ctx, "planning %s for %s", in.Env, rc.Git.Branch)
	if v, ok := os.LookupEnv(sdkserve.ProtocolHandlesEnv); ok {
		return fmt.Errorf("%s is still set to %q", sdkserve.ProtocolHandlesEnv, v)
	}
	held, err := exec.CommandContext(ctx, "sh", "-c",
		`(: <&3) 2>/dev/null && echo 3; (: >&4) 2>/dev/null && echo 4; true`).Output()
	if err != nil {
		return err
	}
	if len(held) > 0 {
		return fmt.Errorf("a subprocess inherited protocol descriptors %q", held)
	}
	build := sparkwing.Job(plan, "build", protocolStepsJob{})
	sparkwing.Job(plan, "deploy", func(context.Context) error { return nil }).Needs(build)
	return nil
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
	os.Args = []string{os.Args[0], "--sw-node-protocol"}
	Main()
}

type entrySession struct {
	t      *testing.T
	cmd    *exec.Cmd
	req    *os.File
	frames *nodeproto.Reader
	stdout bytes.Buffer
	nextID int
	ops    map[string]nodeproto.Op
	proto  protocolSchema
	doc    protocolSchema
	record protocolSchema
}

func startEntry(t *testing.T) *entrySession {
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
		t: t, req: reqW, frames: nodeproto.NewReader(frR), ops: map[string]nodeproto.Op{},
		proto:  loadProtocolSchema(t, "node-protocol.schema.json"),
		doc:    loadProtocolSchema(t, "describe.schema.json"),
		record: loadProtocolSchema(t, "log-record.schema.json"),
	}
	s.cmd = exec.Command(os.Args[0], "-test.run=^TestNodeProtocolEntryProcess$")
	s.cmd.Dir = t.TempDir()
	s.cmd.Env = append(os.Environ(), nodeProtocolEntryEnv+"=1", sdkserve.ProtocolHandlesEnv+"=3,4")
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
	return s
}

// safety: returns the log records that arrived before the reply, so a test
// can see plan code's logging went to the frame pipe.
func (s *entrySession) call(req nodeproto.Request) (nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	s.nextID++
	req.ID = fmt.Sprint(s.nextID)
	line, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.proto.mustValidate(s.t, "request", jsonValue(s.t, line))
	s.ops[req.ID] = req.Op
	if _, err := s.req.Write(append(line, '\n')); err != nil {
		s.t.Fatal(err)
	}
	var logs []map[string]any
	for {
		raw, err := s.frames.ReadLine()
		if err != nil {
			s.t.Fatalf("frame pipe after request %s: %v; stdout: %s", req.ID, err, s.stdout.String())
		}
		frame, err := nodeproto.ParseFrame(raw)
		if err != nil {
			s.t.Fatalf("frame %s: %v", raw, err)
		}
		v := jsonValue(s.t, raw)
		if frame.Kind == nodeproto.KindLog {
			s.record.mustValidate(s.t, "", v)
			logs = append(logs, v.(map[string]any))
			continue
		}
		s.proto.mustValidate(s.t, "reply", v)
		if frame.Reply.ID != req.ID {
			s.t.Fatalf("reply to %s while %s is the only request in flight", frame.Reply.ID, req.ID)
		}
		if frame.Reply.OK {
			def := map[nodeproto.Op]string{nodeproto.OpDescribe: "", nodeproto.OpPlan: "plan"}[req.Op]
			s.doc.mustValidate(s.t, def, jsonValue(s.t, frame.Reply.Result))
		}
		return frame.Reply, logs
	}
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

func jsonValue(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func wantCode(t *testing.T, r nodeproto.Reply, code nodeproto.ErrorCode) {
	t.Helper()
	if r.OK || r.Error == nil || r.Error.Code != code {
		t.Errorf("reply %+v, want a failure with code %s", r, code)
	}
}

func planRequest(args map[string]string) nodeproto.Request {
	return nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: servePipeline, Args: args, Run: &nodeproto.RunContext{
		RunID: "run-1", Trigger: &nodeproto.Trigger{Source: "push"},
		Git: &nodeproto.Git{SHA: "4f2c1e0", Branch: "main", Repo: "acme/app"},
	}}
}

func TestNodeProtocolEntryServesDescribeAndPlan(t *testing.T) {
	s := startEntry(t)

	reply, _ := s.call(nodeproto.Request{Op: nodeproto.OpDescribe})
	var described sdkserve.DescribeDoc
	if err := reply.Decode(&described); err != nil {
		t.Fatal(err)
	}
	if described.Protocol != nodeproto.Version || !slices.ContainsFunc(described.Pipelines, func(p sparkwing.DescribePipeline) bool {
		return p.Name == servePipeline
	}) {
		t.Errorf("describe = %+v, want protocol %s listing %s", described, nodeproto.Version, servePipeline)
	}

	for _, req := range []nodeproto.Request{
		{Op: nodeproto.OpRunNode, Node: "build", Attempt: 1},
		{Op: nodeproto.OpRunStep, Node: "build", Step: "build", Attempt: 1},
		{Op: nodeproto.OpEval, Closure: "deploy/skip_if/0"},
	} {
		reply, _ := s.call(req)
		wantCode(t, reply, nodeproto.CodeUnknownOp)
	}

	reply, logs := s.call(planRequest(map[string]string{"env": "prod"}))
	var doc nodeproto.PlanDoc
	if err := reply.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.RunID != "run-1" || doc.Pipeline != servePipeline || len(doc.Nodes) != 2 {
		t.Errorf("plan = %+v, want run-1's two nodes", doc)
	}
	for _, n := range doc.Nodes {
		if n.SpecHash != "" {
			t.Errorf("node %s carries spec_hash %q, which only the host computes", n.ID, n.SpecHash)
		}
	}
	if len(logs) != 1 || logs[0]["msg"] != "planning prod for main" {
		t.Errorf("plan logged %v on the frame pipe, want the plan's one Info record", logs)
	}

	reply, _ = s.call(planRequest(map[string]string{"env": "prod"}))
	wantCode(t, reply, nodeproto.CodeBadRequest)

	s.end()
	if !strings.Contains(s.stdout.String(), forgedReply) {
		t.Errorf("stdout = %q, want the plan's own print of %s", s.stdout.String(), forgedReply)
	}
}

func TestNodeProtocolEntryRefusesBadPlans(t *testing.T) {
	for name, tc := range map[string]struct {
		req  nodeproto.Request
		want nodeproto.ErrorCode
	}{
		"unknown pipeline": {nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: "nope", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "r"}}, nodeproto.CodeUnknownPipeline},
		"missing flag":     {planRequest(map[string]string{}), nodeproto.CodeInvalidArgs},
		"bad int":          {planRequest(map[string]string{"env": "prod", "count": "many"}), nodeproto.CodeInvalidArgs},
	} {
		t.Run(name, func(t *testing.T) {
			s := startEntry(t)
			reply, _ := s.call(tc.req)
			wantCode(t, reply, tc.want)
			s.end()
		})
	}
}
