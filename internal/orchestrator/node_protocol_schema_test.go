package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/depcache"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
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

// safety: schema properties the Go SDK does not emit yet, per $defs entry; the
// check fails once a listed one is emitted, so the list cannot go stale.
var notYetEmitted = map[string][]string{
	"node":      {"needs_generated", "tools"},
	"modifiers": {"skip_if_budget_ms"},
	"memoKey":   {"const", "env", "files"},
}

// safety: walks the Go type, so a field no sample sets still has to be in the
// schema, and a schema field the type lacks has to be listed in notYetEmitted.
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
	pending := map[string]bool{}
	for _, name := range notYetEmitted[def] {
		pending[name] = true
		if _, ok := props[name]; !ok {
			t.Errorf("notYetEmitted[%q] lists %q, which $defs/%s does not declare", def, name, def)
		}
		if emitted[name] {
			t.Errorf("%s emits %q; remove it from notYetEmitted[%q]", typ, name, def)
		}
	}
	var stale []string
	for name := range props {
		if !emitted[name] && !pending[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		t.Errorf("$defs/%s declares %q, which %s does not emit; emit it or list it in notYetEmitted", def, name, typ)
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
		"plan":           reflect.TypeFor[nodeproto.PlanDoc](),
		"source":         reflect.TypeFor[nodeproto.PlanSource](),
		"secret":         reflect.TypeFor[pipelines.SecretEntry](),
		"concurrencyKey": reflect.TypeFor[nodeproto.PlanConc](),
		"resources":      reflect.TypeFor[nodeproto.PlanResources](),
		"node":           reflect.TypeFor[nodeproto.PlanNode](),
		"consume":        reflect.TypeFor[nodeproto.PlanConsume](),
		"approval":       reflect.TypeFor[nodeproto.PlanApproval](),
		"pipelineRef":    reflect.TypeFor[nodeproto.PlanPipelineRef](),
		"modifiers":      reflect.TypeFor[nodeproto.PlanModifiers](),
		"work":           reflect.TypeFor[nodeproto.PlanWork](),
		"step":           reflect.TypeFor[nodeproto.PlanStep](),
		"stepGroup":      reflect.TypeFor[nodeproto.PlanStepGroup](),
		"cache":          reflect.TypeFor[depcache.Spec](),
		"memoKey":        reflect.TypeFor[nodeproto.PlanMemoKey](),
		"closures":       reflect.TypeFor[nodeproto.PlanClosures](),
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
	build := sparkwing.Step(w, "build", func(context.Context) error { return nil }).
		SkipIf(func(context.Context) bool { return false })
	sparkwing.Step(w, "test", func(context.Context) error { return nil }).Needs(build).Optional()
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
		CacheDir(sparkwing.GoModules()).
		BeforeRun(func(context.Context) error { return nil }).
		AfterRun(func(context.Context, error) {}).
		Verify(func(context.Context) error { return nil }).
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

	empty, err := marshalPlanSnapshot(sparkwing.NewPlan(), sparkwing.RunContext{Pipeline: "demo", RunID: "run-2"}, planSnapshotMeta{})
	if err != nil {
		t.Fatal(err)
	}
	var emptyDoc map[string]any
	if err := json.Unmarshal(empty, &emptyDoc); err != nil {
		t.Fatal(err)
	}
	if nodes, ok := emptyDoc["nodes"].([]any); !ok || len(nodes) != 0 {
		t.Errorf("an empty plan writes nodes as %v, want []", emptyDoc["nodes"])
	}
	if errs := s.validate(s.def(t, "plan"), emptyDoc, "plan"); len(errs) > 0 {
		t.Errorf("an empty plan does not match the schema: %s", strings.Join(errs, "; "))
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

// safety: SDK authors copy the transcripts in docs/node-protocol.md, so every
// request-pipe line and frame-pipe line in them has to match the schemas.
func TestNodeProtocolTranscriptsMatchTheSchemas(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "node-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	proto := loadProtocolSchema(t, "node-protocol.schema.json")
	describe := loadProtocolSchema(t, "describe.schema.json")
	record := loadProtocolSchema(t, "log-record.schema.json")
	type target struct {
		schema protocolSchema
		def    string
	}
	results := map[string]target{
		"describe": {describe, ""},
		"plan":     {describe, "plan"},
		"run_node": {proto, "runResult"},
		"run_step": {proto, "runResult"},
		"eval":     {proto, "evalResult"},
	}
	check := func(s protocolSchema, def string, v any, at string) {
		if errs := s.validate(s.def(t, def), v, at); len(errs) > 0 {
			t.Errorf("docs/node-protocol.md: %s", strings.Join(errs, "; "))
		}
	}
	checked := 0
	for _, block := range strings.Split(string(raw), "```text\n")[1:] {
		block, _, _ = strings.Cut(block, "```")
		ops := map[string]string{}
		for i, line := range strings.Split(strings.TrimSpace(block), "\n") {
			dir, msg := line[:min(3, len(line))], line[min(3, len(line)):]
			var v any
			if err := json.Unmarshal([]byte(msg), &v); err != nil || (dir != "-> " && dir != "<- ") {
				t.Errorf("docs/node-protocol.md: transcript line %q is not a request (\"-> \") or a frame (\"<- \") holding one JSON object", line)
				continue
			}
			obj, _ := v.(map[string]any)
			at := fmt.Sprintf("transcript line %d", i+1)
			checked++
			switch {
			case dir == "-> ":
				check(proto, "request", v, at)
				id, _ := obj["id"].(string)
				if _, dup := ops[id]; dup {
					t.Errorf("docs/node-protocol.md: %s reuses request id %q", at, id)
				}
				ops[id], _ = obj["op"].(string)
			case obj["reply"] != nil:
				check(proto, "reply", v, at)
				op, sent := ops[obj["reply"].(string)]
				if !sent {
					t.Errorf("docs/node-protocol.md: %s answers id %v, which no request in its transcript sent", at, obj["reply"])
					continue
				}
				if result, ok := obj["result"].(map[string]any); ok && obj["ok"] == true {
					check(results[op].schema, results[op].def, result, at+" result")
					if nodes, ok := result["value"].([]any); ok && op == "eval" {
						for _, n := range nodes {
							check(describe, "node", n, at+" generated node")
						}
					}
				}
			default:
				check(record, "", v, at)
			}
		}
	}
	if checked == 0 {
		t.Fatal("found no transcript lines in docs/node-protocol.md")
	}
}
