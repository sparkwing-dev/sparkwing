package main

import (
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestArtifactOperationDocumentsSelectorsAndBoundedPages(t *testing.T) {
	const path = "/api/v1/runs/{run}/jobs/{job}/artifacts"
	op := wireOperations()["GET "+path]
	builder := &schemaBuilder{definitions: schema{}, types: map[string]reflect.Type{}}
	route, ok := capability.LookupRoute("GET", path)
	if !ok {
		t.Fatal("artifact route missing")
	}
	doc := operationDocument(builder, route, path, op, true)
	params := map[string]schema{}
	for _, raw := range doc["parameters"].([]any) {
		p := raw.(schema)
		if p["in"] == "query" {
			params[p["name"].(string)] = p
		}
	}
	for _, name := range []string{"path", "limit", "cursor"} {
		if params[name] == nil {
			t.Errorf("artifact OpenAPI missing %s query selector", name)
		}
	}
}
