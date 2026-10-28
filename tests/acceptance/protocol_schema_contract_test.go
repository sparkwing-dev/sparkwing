package acceptance_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
)

func TestProtocolSchemaContract(t *testing.T) {
	for _, tc := range []struct {
		name, file, def, body string
		valid                 bool
	}{
		{"hello", "node-protocol", "request", `{"id":"1","op":"hello","protocol":2,"engine":{"version":"v1.0.0"},"runner":{"name":"worker","type":"self_hosted","labels":[]}}`, true},
		{"missing_hello_identity", "node-protocol", "request", `{"id":"1","op":"hello","protocol":2}`, false},
		{"plan_request", "node-protocol", "request", `{"id":"2","op":"plan","pipeline":"Build","args":{},"run":{"run_id":"r1","pipeline":"build"},"future":true}`, true},
		{"job_cancel", "node-protocol", "request", `{"id":"3","op":"cancel","job":"build"}`, true},
		{"eval_cancel", "node-protocol", "request", `{"id":"4","op":"cancel","target":"2"}`, true},
		{"mixed_cancel", "node-protocol", "request", `{"id":"4","op":"cancel","target":"2","job":"build"}`, false},
		{"removed_run_node", "node-protocol", "request", `{"id":"5","op":"run_node","node":"build","attempt":1}`, false},
		{"inner_error", "node-protocol", "replyError", `{"code":"future_code","message":"","hint":"retry","details":{"field":"jobs[0].id"},"future":true}`, true},
		{"missing_error_code", "node-protocol", "replyError", `{"message":"failure"}`, false},
		{"double_error_envelope", "node-protocol", "replyError", `{"code":"internal","message":"failure","error":{"code":"internal","message":"nested"}}`, false},
		{"failed_body", "node-protocol", "runResult", `{"outcome":"failed","error":{"code":"internal","message":"body failed"}}`, true},
		{"string_body_error", "node-protocol", "runResult", `{"outcome":"failed","error":"body failed"}`, false},
		{"reply_both_payloads", "node-protocol", "reply", `{"reply":"2","ok":true,"result":{},"error":{"code":"internal","message":"bad"}}`, false},
		{"describe", "describe", "", `{"pipelines":[{"name":"Build","args":[],"risks_by_step":[{"job_id":"build","step_id":"run","labels":[]}]}]}`, true},
		{"pipeline_describe_failure", "describe", "", `{"pipelines":[{"name":"Broken","args":[],"error":{"code":"plan_failed","message":"missing deployment setting"}},{"name":"Build","args":[],"secrets":[{"name":"TOKEN"}]}]}`, true},
		{"flat_plan", "describe", "plan", `{"jobs":[{"id":"build","steps":[{"id":"run"}],"result_step":"run","retry":{"attempts":0},"env":{"MODE":{"value":"test","startup":false}}}],"priority":0}`, true},
		{"omitted_concurrency_defaults", "describe", "plan", `{"jobs":[],"concurrency":[{"group":"deploy"}]}`, true},
		{"zero_concurrency", "describe", "plan", `{"jobs":[],"concurrency":[{"group":"deploy","capacity":0}]}`, false},
		{"retry_boundary", "describe", "plan", `{"jobs":[{"id":"build","retry":{"attempts":10}}]}`, true},
		{"retry_exceeded", "describe", "plan", `{"jobs":[{"id":"build","retry":{"attempts":11}}]}`, false},
		{"memo_boundary", "describe", "plan", `{"jobs":[{"id":"build","memo":{"ttl_ms":2592000000}}]}`, true},
		{"memo_exceeded", "describe", "plan", `{"jobs":[{"id":"build","memo":{"ttl_ms":2592000001}}]}`, false},
		{"explicit_false", "describe", "plan", `{"jobs":[],"source":{"full_history":false,"tags":false}}`, true},
		{"null_optional", "describe", "plan", `{"jobs":[],"source":{"tags":null}}`, false},
		{"annotation_range", "node-protocol", "annotationRequest", `{"level":"warn","message":"check","line":1,"end_line":2,"column":1,"step":"run"}`, true},
		{"annotation_misspelled_level", "node-protocol", "annotationRequest", `{"level":"warning","message":"check"}`, false},
		{"annotation_zero_line", "node-protocol", "annotationRequest", `{"level":"error","message":"check","line":0}`, false},
		{"null_jobs", "describe", "plan", `{"jobs":null}`, false},
		{"host_owned_plan_identity", "describe", "plan", `{"jobs":[],"run_id":"r1"}`, false},
		{"removed_wrapper", "describe", "plan", `{"jobs":[{"id":"build","modifiers":{"retry":1}}]}`, false},
		{"log_additive", "log-record", "", `{"ts":"2026-10-14T00:00:00Z","level":"info","msg":"hello","future":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := conformance.LoadSchema(filepath.Join("..", "..", "docs", "schemas", tc.file+".schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			def, found := schema.Def(tc.def)
			if !found {
				t.Fatalf("missing schema definition %q", tc.def)
			}
			var value any
			if err := json.Unmarshal([]byte(tc.body), &value); err != nil {
				t.Fatal(err)
			}
			failures := schema.Validate(def, value, tc.def)
			if (len(failures) == 0) != tc.valid {
				t.Fatalf("valid=%t, want %t: %s", len(failures) == 0, tc.valid, strings.Join(failures, "; "))
			}
		})
	}
}
