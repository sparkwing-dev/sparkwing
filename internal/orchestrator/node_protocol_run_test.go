//go:build unix

package orchestrator

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func (s *entrySession) send(req nodeproto.Request) string {
	s.t.Helper()
	s.nextID++
	req.ID = fmt.Sprint(s.nextID)
	line, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.proto.mustValidate(s.t, "Request", req)
	if _, err := s.req.Write(append(line, '\n')); err != nil {
		s.t.Fatal(err)
	}
	return req.ID
}

func (s *entrySession) next() (nodeproto.Reply, []map[string]any) {
	s.t.Helper()
	var logs []map[string]any
	for {
		raw, err := s.frames.ReadLine()
		if err != nil {
			s.t.Fatal(err)
		}
		frame, err := nodeproto.ParseFrame(raw)
		if err != nil {
			s.t.Fatal(err)
		}
		if frame.Log != nil {
			s.proto.mustValidate(s.t, "FrameLog", frame.Log)
			var log map[string]any
			if err := json.Unmarshal(raw, &log); err != nil {
				s.t.Fatal(err)
			}
			logs = append(logs, log)
			continue
		}
		reply := *frame.Reply
		s.proto.mustValidate(s.t, "Reply", reply)
		return reply, logs
	}
}

func TestNodeProtocolEntryRunsStepsAndEvaluatesClosures(t *testing.T) {
	s := startEntry(t, "build")
	reply, _ := s.call(planRequest(map[string]string{"env": "prod"}))
	var doc nodeproto.PlanDoc
	if err := reply.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpEval, Closure: doc.Jobs[0].Steps[0].Closures.SkipIf[0], Attempt: 1, Settled: []string{}})
	var evaluation nodeproto.EvalResult
	if err := reply.Decode(&evaluation); err != nil {
		t.Fatal(err)
	}
	if string(evaluation.Value) != "false" || evaluation.Error != nil {
		t.Fatalf("skip_if: %+v", evaluation)
	}
	reply, _ = s.call(nodeproto.Request{Op: nodeproto.OpRunStep, Job: "build", Step: "build", Attempt: 1, Settled: []string{}})
	var result nodeproto.RunResult
	if err := reply.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "success" {
		t.Fatalf("run_step: %+v", result)
	}
	s.end()
}
