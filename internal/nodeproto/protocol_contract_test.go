package nodeproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

func TestParseRequestV2Shapes(t *testing.T) {
	valid := []string{
		`{"id":"1","op":"hello","protocol":2,"engine":{"version":"v1"},"runner":{"name":"local","type":"local","labels":[]}}`,
		`{"id":"2","op":"hello","protocol":2,"engine":{"version":"v1"},"runner":{"name":"local","type":"local","labels":[]},"binding":{"run_id":"r","job_id":"j","routes":{"url":"http://host","bearer":"secret"}}}`,
		`{"id":"3","op":"describe","future":1}`,
		`{"id":"4","op":"plan","pipeline":"p","args":{},"run":{"run_id":"r","pipeline":"p"}}`,
		`{"id":"5","op":"run_step","job":"j","step":"s","attempt":1}`,
		`{"id":"6","op":"eval","closure":"c","input":null}`,
		`{"id":"7","op":"cancel","job":"j","step":"s"}`,
	}
	for _, line := range valid {
		if _, err := ParseRequest([]byte(line)); err != nil {
			t.Errorf("valid request %s: %v", line, err)
		}
	}
	invalid := []string{
		`null`, `[]`, `{"id":null,"op":"describe"}`, `{"id":"","op":"describe"}`,
		`{"id":"1","id":"2","op":"describe"}`,
		`{"id":"1","op":"run_node"}`, `{"id":"1","op":"hello","protocol":1,"engine":{"version":"v1"},"runner":{"name":"local","type":"local","labels":[]}}`,
		`{"id":"1","op":"hello","protocol":2,"engine":{"version":"v1"}}`,
		`{"id":"1","op":"hello","protocol":2,"engine":{"version":"v1"},"runner":{"name":"local","type":"local","labels":[]},"binding":{"run_id":"r","job_id":"j","routes":{"url":"x"}}}`,
		`{"id":"1","op":"describe","pipeline":"x"}`,
		`{"id":"1","op":"plan","pipeline":"p","args":null,"run":{"run_id":"r","pipeline":"p"}}`,
		`{"id":"1","op":"plan","pipeline":"p","args":{},"run":{"run_id":"r","pipeline":"q"}}`,
		`{"id":"1","op":"run_step","job":"j","step":"s","attempt":0}`,
		`{"id":"1","op":"cancel","job":"j","step":"s","attempt":1}`,
		`{"id":"1","op":"eval","closure":"c","failure":{"job":"j","stage":"action","error":{"code":"sdk_error","message":"x"}}}`,
	}
	for _, line := range invalid {
		if _, err := ParseRequest([]byte(line)); err == nil {
			t.Errorf("accepted invalid request %s", line)
		}
	}
}

func TestRequestValidateRejectsCrossOperationFields(t *testing.T) {
	for _, request := range []Request{
		{ID: "1", Op: OpDescribe, Pipeline: "p"},
		{ID: "1", Op: OpCancel, Job: "j", Step: "s", Attempt: 1},
		{ID: "1", Op: OpEval, Closure: "c", Binding: &ProcessBinding{}},
	} {
		if err := request.Validate(); err == nil {
			t.Errorf("accepted contradictory request %+v", request)
		}
	}
	_, err := ParseRequest([]byte(`{"id":"1","op":"hello","protocol":1,"engine":{"version":"v1"},"runner":{"name":"local","type":"local","labels":[]}}`))
	var mismatch *apierr.Error
	if !errors.As(err, &mismatch) || mismatch.Code != apierr.ProtocolMismatch {
		t.Fatalf("major mismatch: %v", err)
	}
	request, err := ParseRequest([]byte(`{"id":"v1","op":"run_node"}`))
	if !errors.As(err, &mismatch) || mismatch.Code != apierr.UnknownOp || request.ID != "v1" {
		t.Fatalf("removed operation: %+v, %v", request, err)
	}
}

func TestParseFrameV2ReplyAndLogWithoutError(t *testing.T) {
	reply, err := ParseFrame([]byte(`{"reply":"1","ok":true,"result":{"x":1}}`))
	if err != nil || reply.Reply == nil || reply.Log != nil {
		t.Fatalf("reply: %+v, %v", reply, err)
	}
	log, err := ParseFrame([]byte(`{"ts":"now","event":"future","attrs":{"x":1}}`))
	if err != nil || log.Log == nil || log.Reply != nil {
		t.Fatalf("log: %+v, %v", log, err)
	}
}

func TestParseFrameV2Shapes(t *testing.T) {
	for _, line := range []string{
		`{"reply":"1","ok":true,"result":{},"future":1}`,
		`{"reply":"1","ok":false,"error":{"code":"future_code","message":"x","future":1}}`,
		`{"ts":"now","event":"future_event","attrs":{"x":1},"future":1}`,
	} {
		if _, err := ParseFrame([]byte(line)); err != nil {
			t.Errorf("valid frame %s: %v", line, err)
		}
	}
	for _, line := range []string{
		`null`, `{"reply":"1","ok":true}`, `{"reply":"1","ok":true,"result":null}`,
		`{"reply":"1","ok":true,"ok":false,"result":{}}`,
		`{"reply":"1","ok":true,"result":{},"error":{"code":"sdk_error","message":"x"}}`,
		`{"reply":"1","ok":false,"result":{},"error":{"code":"sdk_error","message":"x"}}`,
		`{"reply":"1","ok":false,"error":{"error":{"code":"sdk_error","message":"x"}}}`,
		`{"reply":"1","ok":false,"error":{"code":"sdk_error"}}`,
		`{"reply":"1","ok":true,"result":{},"ts":"now"}`,
		`{"ts":null}`, `{"ts":"now","attrs":[]}`, `{"ts":"now","job":"j"}`,
	} {
		if _, err := ParseFrame([]byte(line)); err == nil {
			t.Errorf("accepted invalid frame %s", line)
		}
	}
}

func TestReplyDecodeV2(t *testing.T) {
	for _, tc := range []struct {
		line string
		out  any
		bad  bool
	}{
		{`{"reply":"1","ok":true,"result":{"outcome":"success","output":null}}`, new(RunResult), false},
		{`{"reply":"1","ok":true,"result":{"outcome":"failed","error":{"code":"sdk_error","message":"body"}}}`, new(RunResult), false},
		{`{"reply":"1","ok":true,"result":{"outcome":"other"}}`, new(RunResult), true},
		{`{"reply":"1","ok":true,"result":{"outcome":"failed"}}`, new(RunResult), true},
		{`{"reply":"1","ok":true,"result":{"value":null,"error":{"code":"future_code","message":"closure"}}}`, new(EvalResult), false},
		{`{"reply":"1","ok":true,"result":{"value":null,"error":{"error":{"code":"sdk_error","message":"x"}}}}`, new(EvalResult), true},
	} {
		frame, err := ParseFrame([]byte(tc.line))
		if err != nil {
			t.Fatalf("parse %s: %v", tc.line, err)
		}
		err = frame.Reply.Decode(tc.out)
		if (err != nil) != tc.bad {
			t.Errorf("decode %s: %v", tc.line, err)
		}
	}
	frame, err := ParseFrame([]byte(`{"reply":"1","ok":false,"error":{"code":"unknown_op","message":"rebuild"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var out json.RawMessage
	var wireErr *apierr.Error
	if err := frame.Reply.Decode(&out); !errors.As(err, &wireErr) || wireErr.Code != apierr.UnknownOp {
		t.Fatalf("failed reply: %v", err)
	}
}

func TestReplyDecodeRejectsOversizeOutput(t *testing.T) {
	reply := Reply{ID: "step", OK: true, Result: []byte(`{"outcome":"success","output":"` + strings.Repeat("x", int(limits.NodeOutputBytes)) + `"}`)}
	var result RunResult
	if err := reply.Decode(&result); !errors.Is(err, ErrMalformed) {
		t.Fatalf("oversize output: %v", err)
	}
}
