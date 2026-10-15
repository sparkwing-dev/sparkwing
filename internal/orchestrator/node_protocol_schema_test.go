package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
)

type protocolSchema struct{ *conformance.Schema }

func loadProtocolSchema(t *testing.T, name string) protocolSchema {
	t.Helper()
	s, err := conformance.LoadSchema(filepath.Join("..", "nodeproto", "conformance", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return protocolSchema{s}
}

func (s protocolSchema) mustValidate(t *testing.T, def string, v any) {
	t.Helper()
	d, ok := s.Def(def)
	if !ok {
		t.Fatalf("schema has no $defs/%s", def)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if errs := s.Validate(d, value, def); len(errs) > 0 {
		t.Errorf("%s: %s", def, strings.Join(errs, "; "))
	}
}

func TestProtocolSchemaDeclaresEveryEmittedField(t *testing.T) {
	for _, group := range []struct {
		name  string
		types []reflect.Type
	}{
		{"v2-wire.schema.json", []reflect.Type{reflect.TypeFor[nodeproto.Request](), reflect.TypeFor[nodeproto.Reply](), reflect.TypeFor[nodeproto.FrameLog](), reflect.TypeFor[nodeproto.HelloResult](), reflect.TypeFor[nodeproto.RunResult](), reflect.TypeFor[nodeproto.EvalResult]()}},
		{"v2-documents.schema.json", []reflect.Type{reflect.TypeFor[nodeproto.DescribeDoc](), reflect.TypeFor[nodeproto.PlanDoc](), reflect.TypeFor[nodeproto.JobSpec](), reflect.TypeFor[nodeproto.StepSpec]()}},
	} {
		s := loadProtocolSchema(t, group.name)
		for _, typ := range group.types {
			def, ok := s.Def(typ.Name())
			if !ok {
				t.Fatalf("schema has no %s", typ.Name())
			}
			props := def["properties"].(map[string]any)
			emitted := map[string]bool{}
			for field := range typ.Fields() {
				name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				if name == "" || name == "-" {
					continue
				}
				emitted[name] = true
				if _, ok := props[name]; !ok {
					t.Errorf("%s emits undeclared field %s", typ, name)
				}
			}
			for name := range props {
				if !emitted[name] {
					t.Errorf("%s declares absent field %s", typ, name)
				}
			}
		}
	}
}

func TestNodeProtocolTranscriptsMatchTheSchemas(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "node-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	wire := loadProtocolSchema(t, "v2-wire.schema.json")
	docs := loadProtocolSchema(t, "v2-documents.schema.json")
	checked := 0
	ops := map[string]nodeproto.Op{}
	for _, block := range strings.Split(string(raw), "```text\n")[1:] {
		block, _, _ = strings.Cut(block, "```")
		for i, line := range strings.Split(strings.TrimSpace(block), "\n") {
			t.Run(fmt.Sprintf("line-%d", checked), func(t *testing.T) {
				if len(line) < 3 {
					t.Fatalf("invalid transcript line %d: %q", i+1, line)
				}
				dir, msg := line[:3], []byte(line[3:])
				checked++
				if dir == "-> " {
					req, err := nodeproto.ParseRequest(msg)
					if err != nil {
						t.Fatal(err)
					}
					wire.mustValidate(t, "Request", req)
					if _, exists := ops[req.ID]; exists {
						t.Fatalf("request ID %q reused", req.ID)
					}
					ops[req.ID] = req.Op
					return
				}
				if dir != "<- " {
					t.Fatalf("unknown transcript direction %q", dir)
				}
				frame, err := nodeproto.ParseFrame(msg)
				if err != nil {
					t.Fatal(err)
				}
				if frame.Log != nil {
					wire.mustValidate(t, "FrameLog", frame.Log)
					return
				}
				reply := frame.Reply
				wire.mustValidate(t, "Reply", reply)
				op, ok := ops[reply.ID]
				if !ok {
					t.Fatalf("reply to unknown request %q", reply.ID)
				}
				if !reply.OK {
					return
				}
				var result any
				var schema protocolSchema
				var def string
				switch op {
				case nodeproto.OpHello:
					result, schema, def = &nodeproto.HelloResult{}, wire, "HelloResult"
				case nodeproto.OpDescribe:
					result, schema, def = &nodeproto.DescribeDoc{}, docs, "DescribeDoc"
				case nodeproto.OpPlan:
					result, schema, def = &nodeproto.PlanDoc{}, docs, "PlanDoc"
				case nodeproto.OpRunStep:
					result, schema, def = &nodeproto.RunResult{}, wire, "RunResult"
				case nodeproto.OpEval:
					result, schema, def = &nodeproto.EvalResult{}, wire, "EvalResult"
				case nodeproto.OpCancel:
					return
				default:
					t.Fatalf("unrecognized request %s", op)
				}
				if err := reply.Decode(result); err != nil {
					t.Fatal(err)
				}
				schema.mustValidate(t, def, result)
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no transcript lines")
	}
}
