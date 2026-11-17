package swtags

import (
	"reflect"
	"testing"
)

func TestPublicParseAndCoerce(t *testing.T) {
	type config struct {
		Count    int    `sw:"count,optional" default:"3"`
		Required string `sw:"name,required"`
		Hidden   string
	}
	var fields []FieldSpec
	fields, err := Parse(reflect.TypeFor[*config]())
	if err != nil || len(fields) != 2 || fields[0].Name != "count" || !fields[0].Optional || !fields[0].HasDefault || fields[0].DefaultRaw != "3" || !fields[1].Required {
		t.Fatalf("fields=%+v error=%v", fields, err)
	}
	var out config
	if err := CoerceAssign(reflect.ValueOf(&out.Count).Elem(), "42", "count"); err != nil || out.Count != 42 {
		t.Fatalf("config=%+v error=%v", out, err)
	}
	if err := CoerceAssign(reflect.ValueOf(&out.Count).Elem(), 1.5, "count"); err == nil || out.Count != 42 {
		t.Fatalf("fractional integer=%d error=%v", out.Count, err)
	}
	if _, err := Parse(reflect.TypeFor[string]()); err == nil {
		t.Fatal("non-struct accepted")
	}
	if _, err := Parse(reflect.TypeFor[struct {
		Bad string `sw:"bad,required,optional"`
	}]()); err == nil {
		t.Fatal("conflicting modifiers accepted")
	}
}
