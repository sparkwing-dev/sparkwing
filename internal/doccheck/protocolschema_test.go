package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestPublishedProtocolSchemasMatchFinalWireTypes(t *testing.T) {
	if !checkProtocolSchemas(filepath.Join("..", "..", "docs")) {
		t.Fatal("published schemas differ from the final wire types")
	}
}

func TestProtocolSchemaDriftFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "schemas"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeProtocolSchemas(dir); err != nil {
		t.Fatal(err)
	}
	if !checkProtocolSchemas(dir) {
		t.Fatal("fresh schemas failed drift check")
	}
	path := filepath.Join(dir, "schemas", "describe.schema.json")
	if err := os.WriteFile(path, []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if checkProtocolSchemas(dir) {
		t.Fatal("changed schema passed drift check")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if checkProtocolSchemas(dir) {
		t.Fatal("missing schema passed drift check")
	}
}

func TestProtocolSchemasDeclareFinalBoundaries(t *testing.T) {
	schemas := buildProtocolSchemas()
	defs := schemas["describe"]["$defs"].(protocolSchema)
	for _, def := range []string{"plan", "job", "step", "concurrency", "closures", "envValue", "retry", "approval"} {
		if defs[def].(protocolSchema)["additionalProperties"] != false {
			t.Errorf("%s admits unknown plan fields", def)
		}
	}
	for _, def := range []string{"describe", "pipeline", "arg"} {
		if defs[def].(protocolSchema)["additionalProperties"] != true {
			t.Errorf("%s refuses additive describe fields", def)
		}
	}
	props := defs["plan"].(protocolSchema)["properties"].(protocolSchema)
	for _, field := range []string{"protocol", "pipeline", "run_id", "nodes", "secrets", "requires"} {
		if _, found := props[field]; found {
			t.Errorf("plan declares removed or host-owned %s", field)
		}
	}
	props = defs["job"].(protocolSchema)["properties"].(protocolSchema)
	for _, field := range []string{"modifiers", "work", "spec_hash", "optional_deps", "outputs", "step_groups"} {
		if _, found := props[field]; found {
			t.Errorf("job declares removed %s", field)
		}
	}
	logDefs := schemas["log-record"]["$defs"].(protocolSchema)
	props = logDefs["frameLog"].(protocolSchema)["properties"].(protocolSchema)
	for _, field := range []string{"node", "job", "job_id"} {
		if _, found := props[field]; found {
			t.Errorf("SDK log declares host attribution %s", field)
		}
	}
	for name, schema := range schemas {
		if _, err := json.Marshal(schema); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestProtocolGuideExamplesMatchSchemas(t *testing.T) {
	dir := filepath.Join("..", "..", "docs")
	blocks, err := extract(dir, "json")
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*conformance.Schema{}
	for _, name := range []string{"node-protocol", "describe", "log-record"} {
		schemas[name], err = conformance.LoadSchema(filepath.Join(dir, "schemas", name+".schema.json"))
		if err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	for _, block := range blocks {
		if block.file != "node-protocol.md" {
			continue
		}
		for _, line := range strings.Split(block.body, "\n") {
			var value map[string]any
			if err := json.Unmarshal([]byte(line), &value); err != nil {
				t.Fatal(err)
			}
			schema, def := schemas["node-protocol"], "request"
			switch {
			case value["error"] != nil && value["reply"] == nil:
				inner, err := json.Marshal(value["error"])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := apierr.DecodeInner(inner); err != nil {
					t.Fatal(err)
				}
				checked++
				continue
			case value["ts"] != nil:
				schema, def = schemas["log-record"], "frameLog"
			case value["reply"] != nil:
				def = "reply"
			case value["jobs"] != nil:
				schema, def = schemas["describe"], "plan"
			}
			shape, found := schema.Def(def)
			if !found {
				t.Fatalf("missing %s", def)
			}
			if failures := schema.Validate(shape, value, def); len(failures) != 0 {
				t.Errorf("guide line %d: %v", block.line, failures)
			}
			if result, ok := value["result"].(map[string]any); ok {
				resultDef := "runResult"
				if result["protocol"] != nil {
					resultDef = "helloResult"
				}
				if result["value"] != nil {
					resultDef = "evalResult"
					body, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					frame, err := nodewire.ParseFrame(body)
					if err != nil {
						t.Fatal(err)
					}
					var generated nodewire.EvalResult
					if err := frame.Reply.Decode(&generated); err != nil {
						t.Fatal(err)
					}
					if err := nodeproto.ValidateEvalValue(generated, "generate"); err != nil {
						t.Fatal(err)
					}
					context := nodeproto.ValidationContext{
						Expansion: true, Placement: nodeproto.PlacementLocal, Secrets: secrets.NewMasker(), SourceJob: "build",
						ExistingJobs: []nodewire.JobSpec{{ID: "build", Closures: nodewire.ClosureSpec{Generate: []string{"build/generate/0"}}}},
					}
					if _, err := nodeproto.ValidatePlan(generated.Value, context); err != nil {
						t.Fatal(err)
					}
				}
				shape, found = schema.Def(resultDef)
				if !found {
					t.Fatalf("missing %s", resultDef)
				}
				if failures := schema.Validate(shape, result, resultDef); len(failures) != 0 {
					t.Errorf("guide result at %d: %v", block.line, failures)
				}
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("guide contains no protocol examples")
	}
}

func TestPublishedProtocolGuideMatchesWireFields(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "node-protocol.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !protocolGuideFieldsMatch(string(body)) {
		t.Fatal("protocol guide operation fields differ from the wire types")
	}
	for _, change := range []struct{ from, to string }{
		{"optional `binding`", "optional `binding`, `features`"},
		{"`protocol`, `sdk` |", "`protocol`, `sdk`, `features` |"},
		{"optional `error`, `arguments`, `declarations_known`", "optional `error`"},
		{"| `cancel` |", "| `removed` |"},
	} {
		changed := strings.Replace(string(body), change.from, change.to, 1)
		if changed == string(body) || protocolGuideFieldsMatch(changed) {
			t.Errorf("guide drift was not rejected: %s", change.to)
		}
	}
}

func protocolGuideFieldsMatch(body string) bool {
	results := map[nodewire.Op]reflect.Type{
		nodewire.OpHello: reflect.TypeFor[nodewire.HelloResult](), nodewire.OpDescribe: reflect.TypeFor[nodewire.DescribeDoc](),
		nodewire.OpPlan: reflect.TypeFor[nodewire.PlanDoc](), nodewire.OpRunStep: reflect.TypeFor[nodewire.RunResult](),
		nodewire.OpEval: reflect.TypeFor[nodewire.EvalResult](), nodewire.OpCancel: nil,
	}
	quoted := regexp.MustCompile("`([a-z_]+)`")
	fields := func(cell string) []string {
		var names []string
		for _, match := range quoted.FindAllStringSubmatch(cell, -1) {
			names = append(names, match[1])
		}
		slices.Sort(names)
		return names
	}
	seen := map[nodewire.Op]bool{}
	for _, line := range strings.Split(body, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 5 {
			continue
		}
		op := nodewire.Op(strings.Trim(strings.TrimSpace(cells[1]), "`"))
		result, found := results[op]
		if !found {
			continue
		}
		if seen[op] {
			return false
		}
		seen[op] = true
		request := nodewire.RequestFields(op).Allowed
		slices.Sort(request)
		if !slices.Equal(fields(cells[2]), request) {
			return false
		}
		var want []string
		if result != nil {
			for _, field := range protocolFields(result) {
				want = append(want, strings.Split(field.Tag.Get("json"), ",")[0])
			}
		}
		slices.Sort(want)
		if !slices.Equal(fields(cells[3]), want) {
			return false
		}
	}
	return len(seen) == len(results)
}

// The published request schema and the host's decoder judge the same requests alike.
func TestRequestSchemaAgreesWithTheDecoder(t *testing.T) {
	schema, err := conformance.LoadSchema(filepath.Join("..", "..", "docs", "schemas", "node-protocol.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	shape, _ := schema.Def("request")
	for request, valid := range map[string]bool{
		`{"id":"1","op":"run_step","job":"j","step":"s","attempt":1,"settled":[]}`:                      true,
		`{"id":"1","op":"run_step","job":"j","step":"s","attempt":1}`:                                   false,
		`{"id":"1","op":"eval","closure":"c","attempt":1,"settled":["a"],"input":{}}`:                   true,
		`{"id":"1","op":"eval","closure":"c","job":"j"}`:                                                false,
		`{"id":"1","op":"eval","closure":"c","failure":{"job":"j","stage":"run","error":{"code":"x"}}}`: false,
		`{"id":"1","op":"cancel","target":"2"}`:                                                         true,
		`{"id":"1","op":"plan","pipeline":"p","args":{},"run":{"run_id":"r","pipeline":"p"},"job":"j"}`: false,
	} {
		var value map[string]any
		if err := json.Unmarshal([]byte(request), &value); err != nil {
			t.Fatal(err)
		}
		_, decodeErr := nodewire.ParseRequest([]byte(request))
		if failures := schema.Validate(shape, value, "request"); (len(failures) == 0) != valid || (decodeErr == nil) != valid {
			t.Errorf("%s: schema failures %v, decoder %v; want valid=%v", request, failures, decodeErr, valid)
		}
	}
}
