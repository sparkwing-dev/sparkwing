package sdkserve

import (
	"encoding/json"
	"reflect"
	"testing"
	"unicode/utf8"
)

func FuzzServiceRequestCollections(f *testing.F) {
	f.Add("argument", "value", false)
	f.Add("__proto__", "null", true)
	f.Fuzz(func(t *testing.T, key, value string, explicitEmpty bool) {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			t.Skip("JSON strings require valid UTF-8")
		}
		type collections struct {
			Objects [1]map[string][]string `json:"objects"`
			Child   *ChildRequest          `json:"child"`
			Omitted []string               `json:"omitted,omitempty"`
		}
		input := collections{Objects: [1]map[string][]string{{key: nil}}, Child: &ChildRequest{Pipeline: value}}
		if explicitEmpty {
			input.Objects[0][key] = []string{}
			input.Child.Args = map[string]string{}
		}
		before, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := EncodeServiceRequest(&input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded collections
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if len(decoded.Objects[0]) != 1 || decoded.Objects[0][key] == nil || decoded.Child.Args == nil || decoded.Child.Pipeline != value || decoded.Omitted != nil {
			t.Fatalf("lost values or collection presence: %s", raw)
		}
		after, err := json.Marshal(input)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("encoder changed caller input: before=%s after=%s error=%v", before, after, err)
		}
	})
}
