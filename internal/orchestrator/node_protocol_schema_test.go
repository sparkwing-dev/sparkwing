package orchestrator

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
)

type protocolSchema struct {
	*conformance.Schema
	root map[string]any
}

func loadProtocolSchema(t *testing.T, name string) protocolSchema {
	t.Helper()
	s, err := conformance.LoadSchema(filepath.Join("..", "..", "docs", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	return protocolSchema{Schema: s, root: s.Root}
}

func (s protocolSchema) def(t *testing.T, name string) map[string]any {
	t.Helper()
	d, ok := s.Def(name)
	if !ok {
		t.Fatalf("schema has no $defs/%s", name)
	}
	return d
}

func (s protocolSchema) validate(schema map[string]any, value any, at string) []string {
	return s.Validate(schema, value, at)
}

func asJSONValue(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (s protocolSchema) mustValidate(t *testing.T, def string, v any) {
	t.Helper()
	if errs := s.validate(s.def(t, def), asJSONValue(t, v), def); len(errs) > 0 {
		t.Errorf("%s does not match docs/schemas: %s", def, strings.Join(errs, "; "))
	}
}

// safety: compare every field so a declaration omitted by a sample cannot hide schema drift.
func (s protocolSchema) requireFieldsDeclared(t *testing.T, def string, typ reflect.Type) {
	t.Helper()
	props, _ := s.def(t, def)["properties"].(map[string]any)
	emitted := map[string]bool{}
	for f := range typ.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		emitted[name] = true
		if _, ok := props[name]; !ok {
			t.Errorf("%s emits %q, absent from $defs/%s", typ, name, def)
		}
	}
	for name := range props {
		if !emitted[name] {
			t.Errorf("$defs/%s declares %q, absent from %s", def, name, typ)
		}
	}
}

func TestDescribeSchemaDeclaresEveryEmittedField(t *testing.T) {
	s := loadProtocolSchema(t, "describe.schema.json")
	for def, typ := range map[string]reflect.Type{
		"describe":    reflect.TypeFor[nodeproto.DescribeDoc](),
		"pipeline":    reflect.TypeFor[nodeproto.PipelineDescription](),
		"example":     reflect.TypeFor[nodeproto.UsageExample](),
		"arg":         reflect.TypeFor[nodeproto.ArgumentDescription](),
		"envVar":      reflect.TypeFor[nodeproto.EnvironmentDescription](),
		"stepRisks":   reflect.TypeFor[nodeproto.StepRisks](),
		"plan":        reflect.TypeFor[nodeproto.PlanDoc](),
		"source":      reflect.TypeFor[nodeproto.SourceSpec](),
		"secret":      reflect.TypeFor[nodeproto.SecretDeclaration](),
		"concurrency": reflect.TypeFor[nodeproto.ConcurrencySpec](),
		"resources":   reflect.TypeFor[nodeproto.ResourceSpec](),
		"job":         reflect.TypeFor[nodeproto.JobSpec](),
		"consume":     reflect.TypeFor[nodeproto.ArtifactInput](),
		"approval":    reflect.TypeFor[nodeproto.ApprovalSpec](),
		"retry":       reflect.TypeFor[nodeproto.RetrySpec](),
		"step":        reflect.TypeFor[nodeproto.StepSpec](),
		"cache":       reflect.TypeFor[nodeproto.CacheSpec](),
		"memo":        reflect.TypeFor[nodeproto.MemoSpec](),
		"closures":    reflect.TypeFor[nodeproto.ClosureSpec](),
	} {
		s.requireFieldsDeclared(t, def, typ)
	}
}

func TestLogRecordSchemaDeclaresEveryEmittedField(t *testing.T) {
	s := loadProtocolSchema(t, "log-record.schema.json")
	s.requireFieldsDeclared(t, "frameLog", reflect.TypeFor[nodeproto.FrameLog]())
}

func TestLogRecordsMatchTheSchema(t *testing.T) {
	s := loadProtocolSchema(t, "log-record.schema.json")
	s.mustValidate(t, "", nodeproto.FrameLog{TS: "2026-10-14T00:00:00Z", Level: "info", Message: "hello"})
	services := loadProtocolSchema(t, "node-protocol.schema.json")
	services.mustValidate(t, "annotationRequest", nodeproto.AnnotationRequest{Level: "error", Message: "unused variable", File: "cmd/app/main.go", Line: 42, EndLine: 43, Column: 2, Step: "lint"})
	services.mustValidate(t, "summaryRequest", nodeproto.SummaryRequest{Markdown: "## done", Step: "lint"})
	annotation := map[string]any{"level": "error", "message": "unused variable", "line": 0.0}
	if failures := services.validate(services.def(t, "annotationRequest"), annotation, "annotation"); len(failures) == 0 {
		t.Fatal("schema accepted annotation at line zero")
	}
}
