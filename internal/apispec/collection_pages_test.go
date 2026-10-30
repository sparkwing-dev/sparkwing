package main

import (
	"reflect"
	"slices"
	"testing"
)

func TestCollectionOperationsDeclareCursorAndPageBounds(t *testing.T) {
	for path, op := range wireOperations() {
		if op.collection == "" {
			continue
		}
		t.Run(path, func(t *testing.T) {
			builder := &schemaBuilder{definitions: schema{}, types: map[string]reflect.Type{}}
			body := collectionResponse(builder, op)
			props := body["properties"].(schema)
			for _, field := range []string{op.collection, "next_cursor", "truncated"} {
				if props[field] == nil || !slices.Contains(body["required"].([]string), field) {
					t.Fatalf("missing page field %s: %+v", field, body)
				}
			}
			required := body["required"].([]string)
			for i, name := range required {
				if slices.Contains(required[:i], name) {
					t.Fatalf("duplicate required property %q", name)
				}
			}
			params := map[string]schema{}
			for _, raw := range op.parameters {
				p := raw.(schema)
				params[p["name"].(string)] = p
			}
			if params["cursor"] == nil || !reflect.DeepEqual(params["limit"]["schema"], schema{"type": "integer", "minimum": 1, "maximum": 200, "default": 50}) {
				t.Fatalf("page query contract: %+v", params)
			}
		})
	}
}
