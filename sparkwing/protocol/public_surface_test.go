package nodeproto

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestPublicRequestFieldNamesAreIndependent(t *testing.T) {
	names := RequestFieldNames()
	if !slices.Contains(names, "pipeline") || !slices.Contains(names, "closure") {
		t.Fatal(names)
	}
	names[0] = "changed"
	if RequestFieldNames()[0] == "changed" {
		t.Fatal("caller mutated shared names")
	}
	rule := RequestFields(OpPlan)
	if !slices.Contains(rule.Required, "pipeline") {
		t.Fatal(rule)
	}
	rule.Required[0] = "changed"
	if RequestFields(OpPlan).Required[0] == "changed" {
		t.Fatal("caller mutated shared rule")
	}
}

func TestPublicHostLines(t *testing.T) {
	for _, tc := range []struct {
		line            string
		recognized, bad bool
	}{
		{`{"exec_output":"cmd","stream":"stdout","data":"aGVsbG8="}`, true, false},
		{`{"exec_done":"cmd","exit_code":0}`, true, false},
		{`{"exec_output":"cmd","stream":"invalid"}`, true, true},
		{`{"exec_done":""}`, true, true},
		{`{"exec_done":`, true, true},
		{`{"id":"request","op":"describe"}`, false, false},
	} {
		var line HostLine
		line, recognized, err := ParseHostLine([]byte(tc.line))
		if recognized != tc.recognized || (err != nil) != tc.bad {
			t.Fatalf("line=%s result=%+v recognized=%v error=%v", tc.line, line, recognized, err)
		}
		if recognized && !tc.bad && line.Output == nil && line.Done == nil {
			t.Fatal("missing decoded line")
		}
	}
}

func TestPublicWireTypeLookup(t *testing.T) {
	typ, found := JSONFieldType(reflect.TypeFor[ExecCancel](), "exec_cancel")
	if !found || typ.Kind() != reflect.String {
		t.Fatalf("type=%v found=%v", typ, found)
	}
	if _, found := JSONFieldType(reflect.TypeFor[ExecCancel](), "ExecCancel"); found {
		t.Fatal("accepted Go field spelling")
	}
	var input MemoInput
	if err := input.UnmarshalJSON([]byte(`{"kind":"const","value":"v"}`)); err != nil || input.Kind != "const" || input.Value != "v" {
		t.Fatalf("input=%+v error=%v", input, err)
	}
	if err := input.UnmarshalJSON([]byte(`{"kind":"parts","parts":[9007199254740993]}`)); err != nil || input.Parts[0] != json.Number("9007199254740993") {
		t.Fatalf("literal precision lost: %+v error=%v", input, err)
	}
	west := "west"
	env := EnvironmentDescription{Name: "REGION", Description: "region", Default: &west}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EnvironmentDescription
	if err = json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(env, decoded) {
		t.Fatalf("environment=%+v error=%v", decoded, err)
	}
}

func TestPublicWriterLines(t *testing.T) {
	var output bytes.Buffer
	writer := NewWriter(&output)
	if err := writer.Send(map[string]string{"reply": "ok"}); err != nil || output.String() != `{"reply":"ok"}`+"\n" {
		t.Fatalf("output=%q error=%v", output.String(), err)
	}
}
