package conformance

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

var updateV2Schemas = flag.Bool("update-v2-schemas", false, "write the protocol v2 schema snapshots")

func TestV2SchemaSnapshots(t *testing.T) {
	for _, group := range []struct {
		name  string
		types []any
	}{
		{"v2-wire.schema.json", []any{nodeproto.Request{}, nodeproto.Reply{}, nodeproto.FrameLog{}, nodeproto.HelloResult{}, nodeproto.RunResult{}, nodeproto.EvalResult{}}},
		{"v2-documents.schema.json", []any{nodeproto.DescribeDoc{}, nodeproto.PlanDoc{}}},
	} {
		defs := map[string]any{}
		for _, value := range group.types {
			shape(reflect.TypeOf(value), defs, reflect.TypeOf(value) == reflect.TypeOf(nodeproto.PlanDoc{}))
		}
		root := map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$defs": defs}
		raw, err := json.MarshalIndent(root, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		path := filepath.Join("testdata", group.name)
		if *updateV2Schemas {
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, want) {
			t.Errorf("%s changed; inspect the wire delta before updating the snapshot", path)
		}
	}
}

func shape(t reflect.Type, defs map[string]any, strict bool) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{}
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": shape(t.Elem(), defs, strict)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": shape(t.Elem(), defs, strict)}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		name := t.Name()
		if name == "" {
			panic(fmt.Sprintf("anonymous struct in wire schema: %s", t))
		}
		if _, exists := defs[name]; exists {
			return map[string]any{"$ref": "#/$defs/" + name}
		}
		properties := map[string]any{}
		var required []string
		defs[name] = map[string]any{}
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			key, options, _ := strings.Cut(tag, ",")
			if key == "-" {
				continue
			}
			if key == "" {
				key = field.Name
			}
			properties[key] = shape(field.Type, defs, strict)
			if !strings.Contains(options, "omitempty") && !strings.Contains(options, "omitzero") {
				required = append(required, key)
			}
		}
		object := map[string]any{"type": "object", "properties": properties, "additionalProperties": !strict}
		if len(required) > 0 {
			object["required"] = required
		}
		defs[name] = object
		return map[string]any{"$ref": "#/$defs/" + name}
	default:
		panic(fmt.Sprintf("unsupported wire schema kind: %s", t))
	}
}
