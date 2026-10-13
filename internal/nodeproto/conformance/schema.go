// Package conformance holds the node protocol conformance suite
// (docs/node-protocol.md): a driver that starts any SDK entry with
// --sw-node-protocol and runs a fixed set of sessions against the fixture
// pipeline in testdata/fixture.json, which every SDK implements alike. The
// Go SDK's fixture is ./gofixture. The package itself exports the subset
// JSON Schema check that judges every frame against docs/schemas.
package conformance

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
)

// Schema is one JSON Schema file from docs/schemas.
type Schema struct {
	Root map[string]any
}

// LoadSchema reads and decodes the schema file at path.
func LoadSchema(path string) (*Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Schema{Root: root}, nil
}

// Def returns $defs/name, or the root schema when name is empty.
func (s *Schema) Def(name string) (map[string]any, bool) {
	if name == "" {
		return s.Root, true
	}
	defs, _ := s.Root["$defs"].(map[string]any)
	d, ok := defs[name].(map[string]any)
	return d, ok
}

// Validate checks value, a decoded JSON value, against schema, which is s's
// root or one of its parts, and returns each violation prefixed with its
// path from at. It understands only the keywords docs/schemas uses; any other
// keyword is itself a violation, so a schema it cannot judge never passes.
func (s *Schema) Validate(schema map[string]any, value any, at string) []string {
	var errs []string
	for key, want := range schema {
		switch key {
		case "$schema", "$defs", "title", "description":
		case "$ref":
			ref, _ := want.(string)
			name, ok := strings.CutPrefix(ref, "#/$defs/")
			defs, _ := s.Root["$defs"].(map[string]any)
			target, found := defs[name].(map[string]any)
			if !ok || !found {
				errs = append(errs, fmt.Sprintf("%s: unresolvable $ref %q", at, ref))
				continue
			}
			errs = append(errs, s.Validate(target, value, at)...)
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
					errs = append(errs, s.Validate(sub.(map[string]any), v, at+"."+name)...)
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
					errs = append(errs, s.Validate(extra, v, at+"."+name)...)
				}
			}
		case "items":
			if arr, ok := value.([]any); ok {
				for i, v := range arr {
					errs = append(errs, s.Validate(want.(map[string]any), v, fmt.Sprintf("%s[%d]", at, i))...)
				}
			}
		case "allOf":
			for _, sub := range want.([]any) {
				errs = append(errs, s.Validate(sub.(map[string]any), value, at)...)
			}
		case "if":
			if len(s.Validate(want.(map[string]any), value, at)) == 0 {
				if then, ok := schema["then"].(map[string]any); ok {
					errs = append(errs, s.Validate(then, value, at)...)
				}
			}
		case "then":
		default:
			errs = append(errs, fmt.Sprintf("%s: schema keyword %q is outside the subset this check validates", at, key))
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
