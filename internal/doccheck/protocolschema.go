package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

//go:generate go run . --write-protocol-schemas ../../docs

type protocolSchema map[string]any

type protocolSchemaBuilder struct {
	defs   protocolSchema
	strict bool
}

func (b *protocolSchemaBuilder) value(t reflect.Type) protocolSchema {
	if t == reflect.TypeFor[json.RawMessage]() {
		return protocolSchema{}
	}
	if t == reflect.TypeFor[time.Time]() {
		return protocolSchema{"type": "string"}
	}
	if t.Kind() == reflect.Pointer {
		return b.value(t.Elem())
	}
	switch t.Kind() {
	case reflect.String:
		return protocolSchema{"type": "string"}
	case reflect.Bool:
		return protocolSchema{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return protocolSchema{"type": "integer"}
	case reflect.Float64:
		return protocolSchema{"type": "number"}
	case reflect.Interface:
		return protocolSchema{}
	case reflect.Slice:
		return protocolSchema{"type": "array", "items": b.value(t.Elem())}
	case reflect.Map:
		return protocolSchema{"type": "object", "additionalProperties": b.value(t.Elem())}
	case reflect.Struct:
		name := protocolSchemaName(t)
		if _, exists := b.defs[name]; !exists {
			b.defs[name] = protocolSchema{}
			props := protocolSchema{}
			required := []string{}
			for _, field := range protocolFields(t) {
				tag := strings.Split(field.Tag.Get("json"), ",")
				if tag[0] == "" || tag[0] == "-" {
					continue
				}
				props[tag[0]] = b.value(field.Type)
				if len(tag) == 1 {
					required = append(required, tag[0])
				}
			}
			def := protocolSchema{"type": "object", "properties": props, "additionalProperties": !b.strict}
			if len(required) > 0 {
				def["required"] = required
			}
			b.defs[name] = def
			constrainProtocolSchema(name, def)
		}
		return protocolSchema{"$ref": "#/$defs/" + name}
	default:
		panic(fmt.Sprintf("unsupported protocol schema type %s", t))
	}
}

// safety: an embedded struct's fields are inlined as encoding/json inlines them, so the schema matches the wire.
func protocolFields(t reflect.Type) []reflect.StructField {
	var out []reflect.StructField
	for field := range t.Fields() {
		if field.Anonymous && field.Tag.Get("json") == "" && field.Type.Kind() == reflect.Struct {
			out = append(out, protocolFields(field.Type)...)
			continue
		}
		out = append(out, field)
	}
	return out
}

func protocolSchemaName(t reflect.Type) string {
	names := map[reflect.Type]string{
		reflect.TypeFor[nodewire.SDKIdentity](): "sdk",
		reflect.TypeFor[nodewire.DescribeDoc](): "describe", reflect.TypeFor[nodewire.PipelineDescription](): "pipeline",
		reflect.TypeFor[nodewire.ArgumentDescription](): "arg", reflect.TypeFor[nodewire.EnvironmentDescription](): "envVar",
		reflect.TypeFor[nodewire.UsageExample](): "example", reflect.TypeFor[nodewire.SecretDeclaration](): "secret",
		reflect.TypeFor[nodewire.PlanDoc](): "plan", reflect.TypeFor[nodewire.JobSpec](): "job",
		reflect.TypeFor[nodewire.StepSpec](): "step", reflect.TypeFor[nodewire.ClosureSpec](): "closures",
		reflect.TypeFor[nodewire.ConcurrencySpec](): "concurrency",
		reflect.TypeFor[nodewire.SourceSpec]():      "source", reflect.TypeFor[nodewire.RetrySpec](): "retry",
		reflect.TypeFor[nodewire.MemoSpec](): "memo", reflect.TypeFor[nodewire.CacheSpec](): "cache",
		reflect.TypeFor[nodewire.ApprovalSpec](): "approval", reflect.TypeFor[nodewire.ArtifactInput](): "consume",
		reflect.TypeFor[nodewire.PipelineReference](): "pipelineRef", reflect.TypeFor[nodewire.JobFailure](): "failure",
		reflect.TypeFor[apierr.Error](): "replyError",
	}
	if name, found := names[t]; found {
		return name
	}
	return strings.ToLower(t.Name()[:1]) + t.Name()[1:]
}

func constrainProtocolSchema(name string, def protocolSchema) {
	props := def["properties"].(protocolSchema)
	minimum := func(field string, n int) { props[field].(protocolSchema)["minimum"] = n }
	nonempty := func(fields ...string) {
		for _, f := range fields {
			props[f].(protocolSchema)["minLength"] = 1
		}
	}
	enum := func(field string, values ...string) { props[field].(protocolSchema)["enum"] = values }
	switch name {
	case "request":
		nonempty("id", "pipeline", "job", "step", "closure", "target")
		enum("op", "hello", "describe", "plan", "run_step", "eval", "cancel")
		props["protocol"].(protocolSchema)["const"] = nodewire.Version
		minimum("attempt", 1)
		def["allOf"] = requestSchemaRules()
	case "reply":
		nonempty("reply")
		def["oneOf"] = []any{
			protocolSchema{"properties": protocolSchema{"ok": protocolSchema{"const": true}}, "required": []string{"result"}, "not": protocolSchema{"required": []string{"error"}}},
			protocolSchema{"properties": protocolSchema{"ok": protocolSchema{"const": false}}, "required": []string{"error"}, "not": protocolSchema{"required": []string{"result"}}},
		}
	case "replyError":
		nonempty("code")
		def["not"] = protocolSchema{"required": []string{"error"}}
	case "helloResult":
		props["protocol"].(protocolSchema)["const"] = nodewire.Version
	case "engineIdentity", "sdk":
		nonempty("version")
	case "runContext":
		nonempty("run_id", "pipeline")
	case "runResult":
		enum("outcome", "success", "failed")
	case "annotationRequest":
		enum("level", append([]string{""}, nodeproto.AnnotationLevels...)...)
		for _, f := range []string{"line", "end_line", "column"} {
			minimum(f, 1)
		}
	case "frameLog":
		props["attrs"] = protocolSchema{"type": "object"}
		enum("level", "debug", "info", "warn", "error")
	case "pipeline", "arg", "envVar", "secret", "cache":
		nonempty("name")
		if name == "cache" {
			enum("resolver", nodeproto.CacheResolvers...)
		}
	case "job":
		nonempty("id", "result_step", "on_failure_of")
		enum("failure_policy", "fail_fast", "collect_all")
		for _, f := range []string{"timeout_ms", "no_progress_timeout_ms", "skip_if_budget_ms"} {
			minimum(f, 0)
		}
		props["cores"].(protocolSchema)["minimum"] = nodewire.MinJobCores
	case "step":
		nonempty("id")
	case "concurrency":
		nonempty("group")
		enum("scope", "global", "run", "box")
		enum("on_limit", "queue", "fail", "skip", "cancel_others")
		for _, f := range []string{"capacity", "cost"} {
			minimum(f, 1)
		}
		minimum("queue_timeout_ms", 0)
	case "resources":
		minimum("cores", 0)
		minimum("memory_bytes", 0)
	case "source":
		minimum("depth", 0)
	case "retry":
		props["attempts"].(protocolSchema)["maximum"] = limits.MaxRetries
		minimum("attempts", 0)
		minimum("backoff_ms", 0)
	case "memo":
		props["ttl_ms"].(protocolSchema)["maximum"] = limits.MemoMaxTTL.Milliseconds()
		minimum("ttl_ms", 0)
	case "approval":
		minimum("timeout_ms", 0)
		enum("on_timeout", "fail", "deny", "approve")
	}
}

func requestSchemaRules() []any {
	rules := []any{}
	for _, op := range []nodewire.Op{nodewire.OpHello, nodewire.OpDescribe, nodewire.OpPlan, nodewire.OpRunStep, nodewire.OpEval, nodewire.OpCancel} {
		operation := nodewire.RequestFields(op)
		then := protocolSchema{}
		checks := []any{}
		if len(operation.Required) > 0 {
			then["required"] = operation.Required
		}
		for _, field := range nodewire.RequestFieldNames() {
			if !slices.Contains(operation.Allowed, field) {
				checks = append(checks, protocolSchema{"not": protocolSchema{"required": []string{field}}})
			}
		}
		if op == nodewire.OpCancel {
			checks = append(checks, protocolSchema{"oneOf": []any{
				protocolSchema{"required": []string{"target"}, "not": protocolSchema{"anyOf": []any{protocolSchema{"required": []string{"job"}}, protocolSchema{"required": []string{"step"}}}}},
				protocolSchema{"required": []string{"job"}, "not": protocolSchema{"required": []string{"target"}}},
			}})
		}
		then["allOf"] = checks
		rules = append(rules, protocolSchema{"if": protocolSchema{"properties": protocolSchema{"op": protocolSchema{"const": op}}, "required": []string{"op"}}, "then": then})
	}
	return rules
}

func buildProtocolSchemas() map[string]protocolSchema {
	builder := func(strict bool, types ...reflect.Type) *protocolSchemaBuilder {
		b := &protocolSchemaBuilder{defs: protocolSchema{}, strict: strict}
		for _, t := range types {
			b.value(t)
		}
		return b
	}
	doc := builder(false, reflect.TypeFor[nodewire.DescribeDoc]())
	doc.strict = true
	doc.value(reflect.TypeFor[nodewire.PlanDoc]())
	wire := builder(false, reflect.TypeFor[nodewire.Request](), reflect.TypeFor[nodewire.Reply](), reflect.TypeFor[nodewire.HelloResult](), reflect.TypeFor[nodewire.RunResult](), reflect.TypeFor[nodewire.EvalResult](), reflect.TypeFor[nodewire.AnnotationRequest](), reflect.TypeFor[nodewire.SummaryRequest](), reflect.TypeFor[nodewire.MaskRequest]())
	logs := builder(false, reflect.TypeFor[nodewire.FrameLog]())
	wrap := func(title, description string, b *protocolSchemaBuilder) protocolSchema {
		return protocolSchema{"$schema": "https://json-schema.org/draft/2020-12/schema", "title": title, "description": description, "$defs": b.defs}
	}
	describe := wrap("Sparkwing node protocol v2: describe and plan", "Generated from internal/nodeproto by go generate ./internal/doccheck. Plan shapes are strict; graph, secret, presence and placement checks belong to ValidatePlan.", doc)
	describe["$ref"] = "#/$defs/describe"
	record := wrap("Sparkwing node protocol v2: SDK log record", "Generated from FrameLog. Host assigns job attribution and masks before persistence. Unknown additive fields and events remain readable.", logs)
	record["$ref"] = "#/$defs/frameLog"
	protocol := wrap("Sparkwing node protocol v2: requests and replies", "Generated from internal/nodeproto. Select a $defs entry for each message. Hello negotiates once; other messages carry no version. Stateful ordering, correlation and duplicate-key checks belong to the protocol reader.", wire)
	protocol["oneOf"] = []any{protocolSchema{"$ref": "#/$defs/request"}, protocolSchema{"$ref": "#/$defs/reply"}}
	return map[string]protocolSchema{"describe": describe, "node-protocol": protocol, "log-record": record}
}

func protocolSchemaBytes(schema protocolSchema) ([]byte, error) {
	body, err := json.MarshalIndent(schema, "", "  ")
	return append(body, '\n'), err
}

func writeProtocolSchemas(contentDir string) error {
	for name, schema := range buildProtocolSchemas() {
		body, err := protocolSchemaBytes(schema)
		if err != nil {
			return err
		}
		// #nosec G703 -- the build tool writes fixed schema names under the operator-selected docs directory
		if err := os.WriteFile(filepath.Join(contentDir, "schemas", name+".schema.json"), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func checkProtocolSchemas(contentDir string) bool {
	ok := true
	for name, schema := range buildProtocolSchemas() {
		want, err := protocolSchemaBytes(schema)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return false
		}
		path := filepath.Join(contentDir, "schemas", name+".schema.json")
		// #nosec G703 -- the build tool reads fixed schema names under the operator-selected docs directory
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			fmt.Fprintf(os.Stderr, "protocol-schema: %s differs from the v2 contract; run GOWORK=off go generate ./internal/doccheck\n", path)
			ok = false
		}
	}
	return ok
}
