package main

import (
	"encoding/json"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto/conformance"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	for _, name := range []string{"node-protocol", "describe"} {
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
