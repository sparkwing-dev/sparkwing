package conformance

import (
	"encoding/json"
	"testing"
)

func TestSchemaV2Keywords(t *testing.T) {
	for _, tc := range []struct {
		name, schema, value string
		valid               bool
	}{
		{"one", `{"oneOf":[{"type":"string"},{"type":"number"}]}`, `"x"`, true},
		{"ambiguous", `{"oneOf":[{"type":"number"},{"minimum":0}]}`, `1`, false},
		{"no_alternative", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `false`, false},
		{"either", `{"anyOf":[{"type":"string"},{"type":"number"}]}`, `1`, true},
		{"forbidden", `{"not":{"required":["error"]}}`, `{"error":{}}`, false},
		{"not_matching", `{"not":{"required":["error"]}}`, `{}`, true},
		{"empty_string", `{"minLength":1}`, `""`, false},
		{"unicode_length", `{"minLength":2}`, `"é"`, false},
		{"unicode_pair", `{"minLength":2}`, `"é界"`, true},
		{"maximum_boundary", `{"maximum":10}`, `10`, true},
		{"maximum_exceeded", `{"maximum":10}`, `11`, false},
		{"unknown_keyword", `{"unsupported":true}`, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var schema map[string]any
			var value any
			if err := json.Unmarshal([]byte(tc.schema), &schema); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.value), &value); err != nil {
				t.Fatal(err)
			}
			failures := (&Schema{}).Validate(schema, value, "test")
			if (len(failures) == 0) != tc.valid {
				t.Fatalf("valid=%t, want %t: %v", len(failures) == 0, tc.valid, failures)
			}
		})
	}
}
