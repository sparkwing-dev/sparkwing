package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type protocolSchema struct {
	root map[string]any
}

func loadProtocolSchema(t *testing.T, name string) protocolSchema {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return protocolSchema{root: root}
}

func (s protocolSchema) def(t *testing.T, name string) map[string]any {
	t.Helper()
	if name == "" {
		return s.root
	}
	defs, _ := s.root["$defs"].(map[string]any)
	d, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("schema has no $defs/%s", name)
	}
	return d
}

// safety: a keyword outside the subset fails the test, so a schema this check
// does not understand is never passed silently.
func (s protocolSchema) validate(schema map[string]any, value any, at string) []string {
	var errs []string
	for key, want := range schema {
		switch key {
		case "$schema", "$defs", "title", "description":
		case "$ref":
			ref, _ := want.(string)
			name, ok := strings.CutPrefix(ref, "#/$defs/")
			defs, _ := s.root["$defs"].(map[string]any)
			target, found := defs[name].(map[string]any)
			if !ok || !found {
				errs = append(errs, fmt.Sprintf("%s: unresolvable $ref %q", at, ref))
				continue
			}
			errs = append(errs, s.validate(target, value, at)...)
		case "type":
			if !matchesType(want, value) {
				errs = append(errs, fmt.Sprintf("%s: %v is not of type %v", at, value, want))
			}
		case "enum":
			if !slices.ContainsFunc(want.([]any), func(v any) bool { return reflect.DeepEqual(v, value) }) {
				errs = append(errs, fmt.Sprintf("%s: %v is not one of %v", at, value, want))
			}
		case "const":
			if !reflect.DeepEqual(want, value) {
				errs = append(errs, fmt.Sprintf("%s: %v is not %v", at, value, want))
			}
		case "minimum":
			if n, ok := value.(float64); ok && n < want.(float64) {
				errs = append(errs, fmt.Sprintf("%s: %v is below %v", at, n, want))
			}
		case "required":
			obj, ok := value.(map[string]any)
			for _, name := range want.([]any) {
				if _, has := obj[name.(string)]; ok && !has {
					errs = append(errs, fmt.Sprintf("%s: missing required %q", at, name))
				}
			}
		case "properties":
			obj, _ := value.(map[string]any)
			for name, sub := range want.(map[string]any) {
				if v, has := obj[name]; has {
					errs = append(errs, s.validate(sub.(map[string]any), v, at+"."+name)...)
				}
			}
		case "additionalProperties":
			obj, ok := value.(map[string]any)
			if !ok {
				continue
			}
			props, _ := schema["properties"].(map[string]any)
			for name, v := range obj {
				if _, declared := props[name]; declared {
					continue
				}
				switch extra := want.(type) {
				case bool:
					if !extra {
						errs = append(errs, fmt.Sprintf("%s: undeclared property %q", at, name))
					}
				case map[string]any:
					errs = append(errs, s.validate(extra, v, at+"."+name)...)
				}
			}
		case "items":
			if arr, ok := value.([]any); ok {
				for i, v := range arr {
					errs = append(errs, s.validate(want.(map[string]any), v, fmt.Sprintf("%s[%d]", at, i))...)
				}
			}
		case "allOf":
			for _, sub := range want.([]any) {
				errs = append(errs, s.validate(sub.(map[string]any), value, at)...)
			}
		case "if":
			if len(s.validate(want.(map[string]any), value, at)) == 0 {
				if then, ok := schema["then"].(map[string]any); ok {
					errs = append(errs, s.validate(then, value, at)...)
				}
			}
		case "then":
		default:
			errs = append(errs, fmt.Sprintf("%s: schema keyword %q is outside the subset this test validates", at, key))
		}
	}
	return errs
}

func matchesType(want, value any) bool {
	if list, ok := want.([]any); ok {
		return slices.ContainsFunc(list, func(w any) bool { return matchesType(w, value) })
	}
	switch want {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && n == math.Trunc(n)
	case "null":
		return value == nil
	}
	return false
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

// safety: walks the Go type, so a field no sample sets still has to be in the
// schema, and a schema field the type lacks has to be marked Target.
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
			t.Errorf("%s emits %q, which $defs/%s does not declare; add it to docs/schemas and docs/node-protocol.md", typ, name, def)
		}
	}
	var stale []string
	for name, p := range props {
		desc, _ := p.(map[string]any)["description"].(string)
		if !emitted[name] && !strings.HasPrefix(desc, "Target:") {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("$defs/%s declares %q, which %s does not emit; mark it Target: or remove it", def, name, typ)
	}
}

func TestDescribeSchemaDeclaresEveryEmittedField(t *testing.T) {
	s := loadProtocolSchema(t, "describe.schema.json")
	for def, typ := range map[string]reflect.Type{
		"pipeline":       reflect.TypeFor[sparkwing.DescribePipeline](),
		"example":        reflect.TypeFor[sparkwing.Example](),
		"arg":            reflect.TypeFor[sparkwing.DescribeArg](),
		"envVar":         reflect.TypeFor[sparkwing.EnvVarDoc](),
		"stepRisks":      reflect.TypeFor[sparkwing.DescribeStepRisks](),
		"plan":           reflect.TypeFor[planSnapshot](),
		"source":         reflect.TypeFor[snapshotSource](),
		"secret":         reflect.TypeFor[pipelines.SecretEntry](),
		"concurrencyKey": reflect.TypeFor[snapshotConc](),
		"resources":      reflect.TypeFor[snapshotResources](),
		"node":           reflect.TypeFor[snapshotNode](),
		"consume":        reflect.TypeFor[snapshotConsume](),
		"approval":       reflect.TypeFor[snapshotApproval](),
		"pipelineRef":    reflect.TypeFor[snapshotPipelineRef](),
		"modifiers":      reflect.TypeFor[snapshotModifiers](),
		"work":           reflect.TypeFor[snapshotWork](),
		"step":           reflect.TypeFor[snapshotStep](),
		"spawn":          reflect.TypeFor[snapshotSpawn](),
		"spawnEach":      reflect.TypeFor[snapshotSpawnEach](),
		"stepGroup":      reflect.TypeFor[snapshotStepGroup](),
	} {
		s.requireFieldsDeclared(t, def, typ)
	}
}

func TestLogRecordSchemaDeclaresEveryEmittedField(t *testing.T) {
	s := loadProtocolSchema(t, "log-record.schema.json")
	s.requireFieldsDeclared(t, "", reflect.TypeFor[sparkwing.LogRecord]())
}

type protocolFixtureInputs struct {
	Env   string `flag:"env" required:"true" desc:"Target environment"`
	Count int    `flag:"count" default:"3" desc:"Replicas"`
}

type protocolFixturePipeline struct{ sparkwing.Base }

func (protocolFixturePipeline) ShortHelp() string { return "Deploy the fixture" }

func (protocolFixturePipeline) Examples() []sparkwing.Example {
	return []sparkwing.Example{{Comment: "deploy", Command: "sparkwing run fixture --env prod"}}
}

func (protocolFixturePipeline) EnvVars() []sparkwing.EnvVarDoc {
	return []sparkwing.EnvVarDoc{{Name: "REGION", Description: "cloud region", Default: "us-west-2"}}
}

func (protocolFixturePipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ protocolFixtureInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

type protocolStepsJob struct{}

func (protocolStepsJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
	build := sparkwing.Step(w, "build", func(context.Context) error { return nil })
	sparkwing.Step(w, "test", func(context.Context) error { return nil }).Needs(build)
	sparkwing.JobSpawn(w, "scan", snapshotChildJob{}).Needs(build)
	return nil, nil
}

func TestDescribeAndPlanOutputMatchTheSchema(t *testing.T) {
	s := loadProtocolSchema(t, "describe.schema.json")

	const name = "node-protocol-describe-fixture"
	if _, ok := sparkwing.Lookup(name); !ok {
		sparkwing.Register[protocolFixtureInputs](name, func() sparkwing.Pipeline[protocolFixtureInputs] {
			return protocolFixturePipeline{}
		})
	}
	described, err := sparkwingruntime.DescribeAll()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(described, func(dp sparkwing.DescribePipeline) bool { return dp.Name == name }) {
		t.Fatalf("describe output lacks %s", name)
	}
	for _, dp := range described {
		s.mustValidate(t, "pipeline", dp)
	}

	plan := sparkwing.NewPlan()
	group := sparkwing.NewConcurrencyGroup("db", sparkwing.ConcurrencyLimit{
		Capacity: 2, Scope: sparkwing.ScopeBox, OnLimit: sparkwing.Queue, QueueTimeout: time.Minute,
	})
	build := sparkwing.Job(plan, "build", protocolStepsJob{}).
		Retry(3, sparkwing.RetryBackoff(time.Second), sparkwing.RetryAuto()).
		Timeout(10*time.Minute).
		NoProgressTimeout(time.Minute).
		Requires("linux").
		Prefers("fast").
		Concurrency(group, 1).
		Resources(sparkwing.Cores(2), sparkwing.MemoryGB(1)).
		Env("MODE", "ci").
		Outputs("dist/**").
		SkipIf(func(context.Context) bool { return false }).
		Memoize(func(context.Context) (sparkwing.CacheKey, error) { return sparkwing.Key("build"), nil }, sparkwing.TTL(time.Hour))
	sparkwing.Job(plan, "deploy", func(context.Context) error { return nil }).
		Needs(build).
		Consumes(build).
		Optional().
		ContinueOnError().
		OnFailure("rollback", func(context.Context) error { return nil })
	sparkwing.JobApproval(plan, "approve", sparkwing.ApprovalConfig{Message: "ship it?", Timeout: time.Hour}).Needs(build)

	raw, err := marshalPlanSnapshot(plan, sparkwing.RunContext{Pipeline: "demo", RunID: "run-1"},
		planSnapshotMeta{Secrets: pipelines.SecretsField{{Name: "TOKEN", Required: true}}, PipelineRequires: []string{"linux"}})
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if errs := s.validate(s.def(t, "plan"), doc, "plan"); len(errs) > 0 {
		t.Errorf("plan --json output does not match docs/schemas/describe.schema.json: %s", strings.Join(errs, "; "))
	}

	bad := map[string]any{"pipeline": "demo", "run_id": "r", "nodes": []any{map[string]any{"id": "n", "deps": nil, "retries": 2}}}
	if errs := s.validate(s.def(t, "plan"), bad, "plan"); len(errs) == 0 {
		t.Error("the plan schema accepted an undeclared node field; additionalProperties is not enforced")
	}
}

type capturingLogger struct{ records []sparkwing.LogRecord }

func (c *capturingLogger) Log(level, msg string) {
	c.records = append(c.records, sparkwing.LogRecord{TS: time.Now(), Level: level, Msg: msg})
}

func (c *capturingLogger) Emit(rec sparkwing.LogRecord) { c.records = append(c.records, rec) }

func TestLogRecordsMatchTheSchema(t *testing.T) {
	s := loadProtocolSchema(t, "log-record.schema.json")
	logger := &capturingLogger{}
	ctx := sparkwingruntime.WithLogger(context.Background(), logger)
	ctx = sparkwingruntime.WithNode(ctx, "lint")
	sparkwing.Info(ctx, "hello %s", "world")
	sparkwing.Annotate(ctx, "unused variable")
	sparkwing.Summary(ctx, "## done")
	if len(logger.records) != 3 {
		t.Fatalf("captured %d records, want 3", len(logger.records))
	}
	for _, rec := range logger.records {
		s.mustValidate(t, "", rec)
	}

	pointed := map[string]any{
		"ts": "2026-01-02T03:04:05.123Z", "level": "error", "node": "lint", "event": "node_annotation",
		"msg": "unused variable", "attrs": map[string]any{"message": "unused variable", "level": "error", "file": "cmd/app/main.go", "line": 42.0},
	}
	if errs := s.validate(s.root, pointed, "record"); len(errs) > 0 {
		t.Errorf("an annotation with file and line does not match the schema: %s", strings.Join(errs, "; "))
	}
	pointed["attrs"].(map[string]any)["line"] = 0.0
	if errs := s.validate(s.root, pointed, "record"); len(errs) == 0 {
		t.Error("the schema accepted an annotation at line 0")
	}
}
