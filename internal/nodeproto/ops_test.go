package nodeproto

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestRequestValidate(t *testing.T) {
	cases := []struct {
		req  RequestOld
		code ErrorCode
	}{
		{RequestOld{ID: "1", Op: OpDescribeOld}, ""},
		{RequestOld{ID: "1", Op: OpPlanOld, Pipeline: "ci", Args: map[string]string{}, Run: &RunContextOld{RunID: "r"}}, ""},
		{RequestOld{ID: "1", Op: OpPlanOld, Pipeline: "ci", Run: &RunContextOld{RunID: "r"}}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpPlanOld, Pipeline: "ci", Args: map[string]string{}}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpPlanOld, Pipeline: "ci", Args: map[string]string{}, Run: &RunContextOld{RunID: "r", Trigger: &Trigger{Source: "cron"}}}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpRunNodeOld, Node: "build", Attempt: 1}, ""},
		{RequestOld{ID: "1", Op: OpRunNodeOld, Node: "build", Step: "test", Attempt: 1}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpRunNodeOld, Node: "build"}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpRunNodeOld, Node: "fix", Attempt: 1, Failure: &Failure{Node: "build", Stage: "verify"}}, ""},
		{RequestOld{ID: "1", Op: OpRunNodeOld, Node: "fix", Attempt: 1, Failure: &Failure{Node: "build", Stage: "body"}}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpRunStepOld, Node: "build", Step: "test", Attempt: 2}, ""},
		{RequestOld{ID: "1", Op: OpRunStepOld, Node: "build", Step: "test"}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpRunStepOld, Node: "build"}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpCancelOld, Node: "build", Step: "test"}, ""},
		{RequestOld{ID: "1", Op: OpCancelOld, Node: "build"}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpEvalOld, Closure: "build/skip_if/0"}, ""},
		{RequestOld{ID: "1", Op: OpEvalOld, Closure: "c", Input: json.RawMessage(`{"outcome":"failed"}`)}, ""},
		{RequestOld{ID: "1", Op: OpEvalOld, Closure: "c", Input: json.RawMessage(`[1]`)}, CodeBadRequest},
		{RequestOld{ID: "1", Op: OpEvalOld}, CodeBadRequest},
		{RequestOld{Op: OpDescribeOld}, CodeBadRequest},
		{RequestOld{ID: "1", Op: "nope"}, CodeUnknownOp},
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
		req, err := ParseRequestOld(line)
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
		again, err := ParseRequestOld(enc)
		if err != nil {
			t.Fatalf("re-encoded %s fails: %v", enc, err)
		}
		enc2, _ := json.Marshal(again)
		if !reflect.DeepEqual(enc, enc2) {
			t.Fatalf("round trip drift:\n%s\n%s", enc, enc2)
		}
	})
}
