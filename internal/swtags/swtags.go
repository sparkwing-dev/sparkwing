// Package swtags parses the `sw:"..."` struct tag and assigns typed
// values back to struct fields by reflection. The reflection is shared
// between the sparkwing package (InspectPipelineConfig /
// ResolvePipelineConfig) and internal/sparkwingruntime
// (ResolvePipelineSecrets / DecodePipelineConfig); centralizing it
// here keeps the two import paths from carrying duplicate copies.
//
// Tag grammar:
//
//	`sw:"<name>"`
//	`sw:"<name>,required"`
//	`sw:"<name>,optional"`
//
// Combined with a `default:"..."` tag on the same field to supply a
// fallback when no value layers in. `required` and `default` are
// mutually exclusive.
package swtags

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// FieldSpec captures the parsed sw + default tags on one struct field.
type FieldSpec struct {
	Field      reflect.StructField
	Name       string // sw:"<name>"
	Required   bool
	Optional   bool
	DefaultRaw string // default:"..."; "" when absent
	HasDefault bool
}

// Parse walks the exported fields of t (a struct type) and returns
// the field-level parsed specs. Unexported and untagged fields are
// skipped silently. Conflicts (required + default, required +
// optional) error.
func Parse(t reflect.Type) ([]FieldSpec, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct, got %s", t.Kind())
	}
	out := make([]FieldSpec, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		raw, ok := f.Tag.Lookup("sw")
		if !ok {
			continue
		}
		parts := strings.Split(raw, ",")
		name := strings.TrimSpace(parts[0])
		if name == "" {
			return nil, fmt.Errorf("field %s: sw tag has empty name", f.Name)
		}
		spec := FieldSpec{Field: f, Name: name}
		for _, mod := range parts[1:] {
			switch strings.TrimSpace(mod) {
			case "required":
				spec.Required = true
			case "optional":
				spec.Optional = true
			case "":
				// trailing comma, ignore
			default:
				return nil, fmt.Errorf("field %s: unknown sw modifier %q", f.Name, mod)
			}
		}
		if spec.Required && spec.Optional {
			return nil, fmt.Errorf("field %s: sw tag cannot set both required and optional", f.Name)
		}
		if d, has := f.Tag.Lookup("default"); has {
			spec.DefaultRaw = d
			spec.HasDefault = true
			if spec.Required {
				return nil, fmt.Errorf("field %s: required and default cannot both be set", f.Name)
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

// CoerceAssign converts raw into the Go kind of fv and assigns it.
// Supports string, bool, int family, float family, plus a last-resort
// type-assignable path so yaml-decoded slices/maps land in struct
// fields of matching type.
func CoerceAssign(fv reflect.Value, raw any, fieldName string) error {
	if !fv.CanSet() {
		return fmt.Errorf("field %s: cannot set", fieldName)
	}
	if raw == nil {
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		s, err := toString(raw)
		if err != nil {
			return fmt.Errorf("field %s: %w", fieldName, err)
		}
		fv.SetString(s)
	case reflect.Bool:
		b, err := toBool(raw)
		if err != nil {
			return fmt.Errorf("field %s: %w", fieldName, err)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := toInt64(raw)
		if err != nil {
			return fmt.Errorf("field %s: %w", fieldName, err)
		}
		if fv.OverflowInt(n) {
			return fmt.Errorf("field %s: value %d overflows %s", fieldName, n, fv.Type())
		}
		fv.SetInt(n)
	case reflect.Float32, reflect.Float64:
		x, err := toFloat64(raw)
		if err != nil {
			return fmt.Errorf("field %s: %w", fieldName, err)
		}
		fv.SetFloat(x)
	default:
		rv := reflect.ValueOf(raw)
		if rv.Type().AssignableTo(fv.Type()) {
			fv.Set(rv)
			return nil
		}
		return fmt.Errorf("field %s: cannot assign %T to %s", fieldName, raw, fv.Type())
	}
	return nil
}

func toString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	}
	return "", fmt.Errorf("expected string-compatible value, got %T", v)
}

func toBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		return strconv.ParseBool(t)
	}
	return false, fmt.Errorf("expected bool, got %T", v)
}

func toInt64(v any) (int64, error) {
	switch t := v.(type) {
	case int:
		return int64(t), nil
	case int8:
		return int64(t), nil
	case int16:
		return int64(t), nil
	case int32:
		return int64(t), nil
	case int64:
		return t, nil
	case uint, uint8, uint16, uint32, uint64:
		rv := reflect.ValueOf(t)
		return int64(rv.Uint()), nil
	case float64:
		if t != float64(int64(t)) {
			return 0, fmt.Errorf("expected integer, got fractional %v", t)
		}
		return int64(t), nil
	case string:
		return strconv.ParseInt(t, 10, 64)
	}
	return 0, fmt.Errorf("expected integer, got %T", v)
}

func toFloat64(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case float32:
		return float64(t), nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case string:
		return strconv.ParseFloat(t, 64)
	}
	return 0, fmt.Errorf("expected float, got %T", v)
}
