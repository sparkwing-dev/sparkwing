package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	wire "github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func schemaDefinition(t *testing.T, b *schemaBuilder, value schema) schema {
	t.Helper()
	if ref, ok := value["$ref"].(string); ok {
		return b.definitions[strings.TrimPrefix(ref, "#/components/schemas/")].(schema)
	}
	return value
}

func TestSchemasDescribeSharedJSONInsteadOfStoreRows(t *testing.T) {
	for _, example := range []any{
		wire.AnnotationWrite{Annotation: wire.Annotation{Level: "error", Message: "failed"}, StepID: "test"},
		wire.QueuePriorityResult{RunID: "run"},
		wire.RunDetail{},
		wire.ScheduleJob{},
		wire.DataUploadRequest{},
		wire.TokenCreate{},
	} {
		b := schemaBuilder{definitions: schema{}}
		definition := schemaDefinition(t, &b, b.value(reflect.TypeOf(example)))
		if b.err != nil {
			t.Fatal(b.err)
		}
		body, err := json.Marshal(example)
		if err != nil {
			t.Fatal(err)
		}
		var serialized map[string]any
		if err := json.Unmarshal(body, &serialized); err != nil {
			t.Fatal(err)
		}
		properties := definition["properties"].(schema)
		for key := range serialized {
			if properties[key] == nil {
				t.Errorf("%T schema omits serialized %s", example, key)
			}
		}
		if required, ok := definition["required"].([]string); ok {
			for _, key := range required {
				if _, ok := serialized[key]; !ok {
					t.Errorf("%T requires omitted JSON field %s", example, key)
				}
			}
		}
		for _, hidden := range []string{"idempotency_key", "env_json", "capability"} {
			if properties[hidden] != nil {
				t.Errorf("%T publishes hidden field %s", example, hidden)
			}
		}
	}
}

type shapeExample struct {
	Raw    json.RawMessage `json:"raw"`
	Bytes  []byte          `json:"bytes"`
	When   time.Time       `json:"when"`
	Zero   *int            `json:"zero,omitempty"`
	Count  int             `json:"count,string"`
	Rows   []string        `json:"rows"`
	Labels map[string]int  `json:"labels"`
	Hidden string          `json:"-"`
}

func TestSchemaPreservesOmissionNullAndEncodedShapes(t *testing.T) {
	b := schemaBuilder{definitions: schema{}}
	definition := schemaDefinition(t, &b, b.value(reflect.TypeFor[shapeExample]()))
	properties := definition["properties"].(schema)
	if len(properties) != 7 {
		t.Fatalf("properties: %v", properties)
	}
	if len(properties["raw"].(schema)) != 0 {
		t.Fatal("raw JSON was constrained to a byte string")
	}
	if properties["when"].(schema)["format"] != "date-time" || properties["count"].(schema)["type"] != "string" {
		t.Fatal("custom JSON serialization shape lost")
	}
	for _, name := range []string{"bytes", "zero", "rows", "labels"} {
		branches := properties[name].(schema)["anyOf"].([]any)
		if branches[1].(schema)["type"] != "null" {
			t.Fatalf("%s lost null", name)
		}
	}
	bytes := properties["bytes"].(schema)["anyOf"].([]any)[0].(schema)
	if bytes["type"] != "string" || bytes["contentEncoding"] != "base64" {
		t.Fatal("byte slices are base64 JSON strings")
	}
	required := definition["required"].([]string)
	if slices.Contains(required, "zero") || len(required) != 6 {
		t.Fatalf("required: %v", required)
	}
}

func TestSchemasRefuseUnsupportedJSONBeforePublishing(t *testing.T) {
	for _, kind := range []reflect.Type{reflect.TypeFor[chan int](), reflect.TypeFor[map[bool]string]()} {
		b := schemaBuilder{definitions: schema{}}
		b.value(kind)
		if b.err == nil {
			t.Fatalf("unsupported shape %s succeeded", kind)
		}
	}
	b := schemaBuilder{definitions: schema{}}
	first := schemaDefinition(t, &b, b.value(reflect.TypeFor[struct{ A string }]()))
	second := schemaDefinition(t, &b, b.value(reflect.TypeFor[struct{ B int }]()))
	if first["properties"].(schema)["A"] == nil || second["properties"].(schema)["B"] == nil {
		t.Fatal("anonymous shapes borrowed one another's fields")
	}
}
