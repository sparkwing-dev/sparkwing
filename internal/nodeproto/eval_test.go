package nodeproto

import (
	"encoding/json"
	"testing"
)

func TestEvalValueKinds(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		valid   []string
		invalid []string
	}{
		{"skip_if", []string{`true`, `false`}, []string{`null`, `"true"`, `1`, `{}`}},
		{"memo_key", []string{`"key"`, `null`, `true`, `[]`, `{}`}, []string{`""`}},
		{"before_run", []string{`null`}, []string{`false`, `""`, `[]`, `{}`}},
		{"after_run", []string{`null`}, []string{`false`, `""`, `[]`, `{}`}},
		{"verify", []string{`null`}, []string{`false`, `""`, `[]`, `{}`}},
		{"generate", []string{`[]`, `[{"id":"child","steps":[{"id":"run"}]}]`}, []string{`null`, `true`, `{}`, `[null]`, `[1]`}},
		{"unknown", nil, []string{`null`}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			for _, raw := range tc.valid {
				if err := (EvalResult{Value: json.RawMessage(raw)}).ValidateValue(tc.kind); err != nil {
					t.Errorf("valid value %s: %v", raw, err)
				}
			}
			for _, raw := range append(tc.invalid, "", "null null") {
				if err := (EvalResult{Value: json.RawMessage(raw)}).ValidateValue(tc.kind); err == nil {
					t.Errorf("accepted invalid value %s", raw)
				}
			}
		})
	}
}
