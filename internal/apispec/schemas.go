package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type schema map[string]any

type schemaBuilder struct {
	definitions schema
	types       map[string]reflect.Type
	err         error
}

var componentCharacters = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func (b *schemaBuilder) value(t reflect.Type) schema {
	if t == reflect.TypeFor[json.RawMessage]() || t.Kind() == reflect.Interface {
		return schema{}
	}
	if t == reflect.TypeFor[time.Time]() {
		return schema{"type": "string", "format": "date-time"}
	}
	if t.Kind() == reflect.Pointer {
		return nullable(b.value(t.Elem()))
	}
	if t.Implements(reflect.TypeFor[json.Marshaler]()) || reflect.PointerTo(t).Implements(reflect.TypeFor[json.Marshaler]()) {
		b.err = fmt.Errorf("JSON shape requires an explicit schema for custom marshaler %s", t)
		return schema{}
	}
	switch t.Kind() {
	case reflect.String:
		return schema{"type": "string"}
	case reflect.Bool:
		return schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return schema{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return schema{"type": "number"}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nullable(schema{"type": "string", "contentEncoding": "base64"})
		}
		return nullable(schema{"type": "array", "items": b.value(t.Elem())})
	case reflect.Array:
		return schema{"type": "array", "items": b.value(t.Elem()), "minItems": t.Len(), "maxItems": t.Len()}
	case reflect.Map:
		if t.Key().Kind() != reflect.String && (t.Key().Kind() < reflect.Int || t.Key().Kind() > reflect.Uint64) {
			b.err = fmt.Errorf("unsupported JSON map key %s", t.Key())
		}
		return nullable(schema{"type": "object", "additionalProperties": b.value(t.Elem())})
	case reflect.Struct:
		if t.Name() == "" {
			properties := schema{}
			required := []string{}
			b.fields(t, properties, &required)
			return schema{"type": "object", "properties": properties, "required": required, "additionalProperties": true}
		}
		name := componentCharacters.ReplaceAllString(strings.TrimPrefix(t.PkgPath(), "github.com/sparkwing-dev/sparkwing/")+"."+t.Name(), ".")
		if b.types == nil {
			b.types = map[string]reflect.Type{}
		}
		if previous, ok := b.types[name]; ok && previous != t {
			b.err = fmt.Errorf("schema component collision between %s and %s", previous, t)
		}
		b.types[name] = t
		if _, ok := b.definitions[name]; !ok {
			b.definitions[name] = schema{}
			properties := schema{}
			required := []string{}
			b.fields(t, properties, &required)
			definition := schema{"type": "object", "properties": properties, "additionalProperties": true, "x-sparkwing-go-type": t.String()}
			if len(required) > 0 {
				definition["required"] = required
			}
			b.definitions[name] = definition
		}
		return schema{"$ref": "#/components/schemas/" + name}
	default:
		b.err = fmt.Errorf("unsupported JSON shape %s", t)
		return schema{}
	}
}

func (b *schemaBuilder) fields(t reflect.Type, properties schema, required *[]string) {
	for field := range t.Fields() {
		if !field.IsExported() {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")
		if tag[0] == "-" {
			continue
		}
		if field.Anonymous && tag[0] == "" && field.Type.Kind() == reflect.Struct {
			b.fields(field.Type, properties, required)
			continue
		}
		name := tag[0]
		if name == "" {
			name = field.Name
		}
		if _, exists := properties[name]; exists {
			b.err = fmt.Errorf("ambiguous JSON field %s.%s", t, name)
			continue
		}
		value := b.value(field.Type)
		optional := false
		for _, option := range tag[1:] {
			if option == "omitempty" || option == "omitzero" {
				optional = true
			}
			if option == "string" {
				value = schema{"type": "string"}
			}
		}
		properties[name] = value
		if !optional {
			*required = append(*required, name)
		}
	}
}

func nullable(value schema) schema {
	return schema{"anyOf": []any{value, schema{"type": "null"}}}
}
