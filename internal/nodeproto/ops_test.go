package nodeproto

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRequestValidate(t *testing.T) {
	cases := []struct {
		req  Request
		code ErrorCode
	}{
		{Request{ID: "1", Op: OpDescribe}, ""},
		{Request{ID: "1", Op: OpPlan, Pipeline: "ci", Args: map[string]string{}, Run: &RunContext{RunID: "r"}}, ""},
		{Request{ID: "1", Op: OpPlan, Pipeline: "ci", Run: &RunContext{RunID: "r"}}, CodeBadRequest},
		{Request{ID: "1", Op: OpPlan, Pipeline: "ci", Args: map[string]string{}}, CodeBadRequest},
		{Request{ID: "1", Op: OpPlan, Pipeline: "ci", Args: map[string]string{}, Run: &RunContext{RunID: "r", Trigger: &Trigger{Source: "cron"}}}, CodeBadRequest},
		{Request{ID: "1", Op: OpRunNode, Node: "build", Attempt: 1}, ""},
		{Request{ID: "1", Op: OpRunNode, Node: "build", Step: "test", Attempt: 1}, CodeBadRequest},
		{Request{ID: "1", Op: OpRunNode, Node: "build"}, CodeBadRequest},
		{Request{ID: "1", Op: OpRunNode, Node: "fix", Attempt: 1, Failure: &Failure{Node: "build", Stage: "verify"}}, ""},
		{Request{ID: "1", Op: OpRunNode, Node: "fix", Attempt: 1, Failure: &Failure{Node: "build", Stage: "body"}}, CodeBadRequest},
		{Request{ID: "1", Op: OpRunStep, Node: "build", Step: "test", Attempt: 2}, ""},
		{Request{ID: "1", Op: OpRunStep, Node: "build", Step: "test"}, CodeBadRequest},
		{Request{ID: "1", Op: OpRunStep, Node: "build"}, CodeBadRequest},
		{Request{ID: "1", Op: OpEval, Closure: "build/skip_if/0"}, ""},
		{Request{ID: "1", Op: OpEval, Closure: "c", Input: json.RawMessage(`{"outcome":"failed"}`)}, ""},
		{Request{ID: "1", Op: OpEval, Closure: "c", Input: json.RawMessage(`[1]`)}, CodeBadRequest},
		{Request{ID: "1", Op: OpEval}, CodeBadRequest},
		{Request{Op: OpDescribe}, CodeBadRequest},
		{Request{ID: "1", Op: "nope"}, CodeUnknownOp},
	}
	for _, c := range cases {
		err := c.req.Validate()
		var perr *Error
		if c.code == "" && err != nil || c.code != "" && (!errors.As(err, &perr) || perr.Code != c.code) {
			t.Errorf("%+v: err %v, want code %q", c.req, err, c.code)
		}
	}
}

func FuzzParseRequest(f *testing.F) {
	for _, s := range []string{
		`{"id":"1","op":"describe"}`,
		`{"id":"2","op":"plan","pipeline":"ci","args":{"env":"prod"},"run":{"run_id":"r1","git":{"sha":"abc","branch":"main"},"trigger":{"source":"webhook","user":"u","pull_request":{"number":7,"head_sha":"def"}},"started_at":"2026-01-02T03:04:05Z","dry_run":true}}`,
		`{"id":"3","op":"run_node","node":"fix","attempt":2,"failure":{"node":"build","stage":"action","message":"exit 1"}}`,
		`{"id":"4","op":"run_step","node":"build","step":"test","attempt":1}`,
		`{"id":"7","op":"plan","pipeline":"ci","args":{},"run":{"run_id":"r"}}`,
		`{"id":"5","op":"eval","closure":"build/verify/0","input":{"x":1}}`,
		`{"id":5}`, `[]`, `{"id":"6","op":"eval","closure":"c","input":null}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, line []byte) {
		req, err := ParseRequest(line)
		if err != nil {
			var perr *Error
			if !errors.Is(err, ErrMalformed) && !errors.As(err, &perr) {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		enc, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("re-encode %+v: %v", req, err)
		}
		again, err := ParseRequest(enc)
		if err != nil {
			t.Fatalf("re-encoded %s fails: %v", enc, err)
		}
		enc2, _ := json.Marshal(again)
		if !reflect.DeepEqual(enc, enc2) {
			t.Fatalf("round trip drift:\n%s\n%s", enc, enc2)
		}
	})
}
