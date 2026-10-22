package sparkwing

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/argvalue"
)

// Pipeline is the canonical pipeline shape: every pipeline declares a
// typed Inputs struct and populates a [Plan] that the SDK constructs
// and passes in. Pipelines that take no flags use [NoInputs] and are
// registered with [Register]; the [RunContext] argument carries the
// run id and pipeline name for the Plan body.
//
// The convention is to import sparkwing under the short alias `sw`:
//
//	import sw "github.com/sparkwing-dev/sparkwing/sparkwing"
//
//	type Inputs struct {
//	    SkipTests bool   `flag:"skip-tests" desc:"skip the test suite"`
//	    Target    string `flag:"target" default:"local" desc:"deploy target"`
//	}
//
//	type MyPipe struct{}
//	func (MyPipe) Plan(ctx context.Context, plan *sw.Plan, in Inputs, rc sw.RunContext) error {
//	    sw.Job(plan, "lint", &Lint{})
//	    return nil
//	}
//
//	func init() {
//	    sw.Register[Inputs]("my-pipe", func() sw.Pipeline[Inputs] {
//	        return MyPipe{}
//	    })
//	}
//
// Plan must be pure-declarative: build the DAG and return nil. Calling
// sparkwing.Bash / Exec, anything in sparkwing/docker or sparkwing/git,
// or any other ctx-taking helper that touches state from inside Plan()
// panics at runtime. Move the work into a Job's Work() body and surface
// the result via sparkwing.Step + Ref[T].
//
// Anonymous embedded structs in the Inputs type are walked
// recursively when the schema is built, so flag-bundles can be
// shared across pipelines:
//
//	type SkipFilterArgs struct {
//	    Skip string `flag:"skip" desc:"comma-separated job names to skip"`
//	    Only string `flag:"only" desc:"comma-separated job names to run exclusively"`
//	}
//
//	type ReleaseArgs struct {
//	    Version string `flag:"version" desc:"release tag"`
//	    SkipFilterArgs   // --skip / --only become first-class flags
//	}
//
// Per Go embedding semantics, an outer flag name shadows an inner
// one with the same name (the outermost declaration wins).
type Pipeline[T any] interface {
	Plan(ctx context.Context, plan *Plan, in T, rc RunContext) error
}

// NoInputs is the empty-struct convention for pipelines that take no
// flags.
type NoInputs struct{}

// InputSchema is the resolved description of a pipeline's Inputs
// struct: one InputField per declared flag, plus a flag indicating
// whether a `flag:",extra"` bag field is present.
type inputSchema struct {
	Fields []inputField
	Extra  bool
}

// InputField is one pipeline-flag description, parsed once at
// registration time. Drives CLI parsing, --help rendering, schema
// introspection, shell completion, dashboard run-form, and MCP tool
// definitions.
type inputField struct {
	Name        string // flag name (no `--` prefix)
	Short       string // optional one-letter alias
	GoName      string // original Go field name
	Type        string // "string" | "bool" | "int" | "int64" | "float64" | "duration" | "[]string"
	Default     string // raw default value as written in the tag
	Description string
	Required    bool
	Secret      bool     // mask in logs / dashboard
	Enum        []string // allowed values; empty means unconstrained

	fieldIndex []int
	isExtraBag bool
}

type fieldKind int

const (
	kindUnsupported fieldKind = iota
	kindString
	kindBool
	kindInt
	kindInt64
	kindFloat64
	kindDuration
	kindStringSlice
	kindExtraMap
)

const maxEmbedDepth = 8

func parseInputsSchema(t reflect.Type) (inputSchema, error) {
	if t == nil {
		return inputSchema{}, nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return inputSchema{}, fmt.Errorf("Inputs type must be a struct, got %s", t.Kind())
	}
	var schema inputSchema
	seenName := map[string]string{}
	if err := walkInputFields(t, nil, &schema, seenName, 0); err != nil {
		return schema, err
	}
	return schema, nil
}

func walkInputFields(
	t reflect.Type,
	parentIndex []int,
	schema *inputSchema,
	seenName map[string]string,
	depth int,
) error {
	if depth > maxEmbedDepth {
		return fmt.Errorf("anonymous-embed recursion exceeded depth %d at %s", maxEmbedDepth, t)
	}
	for i := range t.NumField() {
		f := t.Field(i)
		idxPath := append(append([]int{}, parentIndex...), i)

		if f.Anonymous {
			if _, hasFlag := f.Tag.Lookup("flag"); !hasFlag {
				ft := f.Type
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					if err := walkInputFields(ft, idxPath, schema, seenName, depth+1); err != nil {
						return err
					}
				}
				continue
			}
		}

		if !f.IsExported() {
			continue
		}
		flagTag, hasFlag := f.Tag.Lookup("flag")
		if !hasFlag {
			continue
		}

		if flagTag == ",extra" {
			if hasAnyOtherTag(f) {
				return fmt.Errorf("field %s: flag:\",extra\" cannot combine with other tags on the same field", f.Name)
			}
			if k := classifyKind(f.Type); k != kindExtraMap {
				return fmt.Errorf("field %s: flag:\",extra\" requires map[string]string, got %s", f.Name, f.Type)
			}
			if schema.Extra {
				return fmt.Errorf("field %s: at most one flag:\",extra\" field allowed per Inputs struct", f.Name)
			}
			schema.Extra = true
			schema.Fields = append(schema.Fields, inputField{
				GoName:     f.Name,
				fieldIndex: idxPath,
				isExtraBag: true,
			})
			continue
		}

		field := inputField{
			Name:       flagTag,
			GoName:     f.Name,
			fieldIndex: idxPath,
		}
		if field.Name == "" {
			return fmt.Errorf("field %s: flag:\"\" must be either a name or \",extra\"", f.Name)
		}
		if _, dup := seenName[field.Name]; dup && depth > 0 {
			continue
		}
		seenName[field.Name] = f.Name

		field.Description = f.Tag.Get("desc")
		field.Default = f.Tag.Get("default")
		field.Required = boolTag(f.Tag.Get("required"))
		field.Secret = boolTag(f.Tag.Get("secret"))
		field.Short = f.Tag.Get("short")
		if enumTag := f.Tag.Get("enum"); enumTag != "" {
			field.Enum = strings.Split(enumTag, ",")
		}

		kind := classifyKind(f.Type)
		switch kind {
		case kindString:
			field.Type = "string"
		case kindBool:
			field.Type = "bool"
		case kindInt:
			field.Type = "int"
		case kindInt64:
			field.Type = "int64"
		case kindFloat64:
			field.Type = "float64"
		case kindDuration:
			field.Type = "duration"
		case kindStringSlice:
			field.Type = "[]string"
		case kindExtraMap:
			return fmt.Errorf("field %s: map[string]string is only supported with flag:\",extra\"", f.Name)
		default:
			return fmt.Errorf("field %s: unsupported type %s (supported: string, bool, int, int64, float64, time.Duration, []string)", f.Name, f.Type)
		}

		schema.Fields = append(schema.Fields, field)
	}
	return nil
}

func populateInputs(schema inputSchema, dst reflect.Value, m map[string]string) error {
	byName := map[string]int{}
	for i, f := range schema.Fields {
		if f.isExtraBag {
			continue
		}
		byName[f.Name] = i
	}

	for _, f := range schema.Fields {
		if f.isExtraBag {
			continue
		}
		raw, present := m[f.Name]
		if !present {
			continue
		}
		fv, err := fieldByIndexAlloc(dst, f.fieldIndex)
		if err != nil {
			return fmt.Errorf("--%s: %w", f.Name, err)
		}
		if err := setField(fv, f, raw); err != nil {
			return fmt.Errorf("--%s: %w", f.Name, err)
		}
	}

	if !schema.Extra {
		return nil
	}
	for _, f := range schema.Fields {
		if !f.isExtraBag {
			continue
		}
		bag, err := fieldByIndexAlloc(dst, f.fieldIndex)
		if err != nil {
			return err
		}
		if bag.IsNil() {
			bag.Set(reflect.MakeMap(bag.Type()))
		}
		for k, v := range m {
			if _, ok := byName[k]; ok {
				continue
			}
			bag.SetMapIndex(reflect.ValueOf(k), reflect.ValueOf(v))
		}
		break
	}
	return nil
}

func readFieldByIndex(v reflect.Value, idx []int) (reflect.Value, bool) {
	for i, n := range idx {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(n)
	}
	return v, true
}

func fieldByIndexAlloc(v reflect.Value, idx []int) (reflect.Value, error) {
	for i, n := range idx {
		if i > 0 && v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, fmt.Errorf("cannot allocate embedded pointer at index %v", idx[:i])
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(n)
	}
	return v, nil
}

func setField(fv reflect.Value, f inputField, raw string) error {
	if !fv.CanSet() {
		return fmt.Errorf("field not settable")
	}
	switch classifyKind(fv.Type()) {
	case kindString:
		fv.SetString(raw)
	case kindBool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid bool %q", raw)
		}
		fv.SetBool(b)
	case kindInt:
		n, err := strconv.ParseInt(raw, 10, 0)
		if err != nil {
			return fmt.Errorf("invalid int %q", raw)
		}
		fv.SetInt(n)
	case kindInt64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int64 %q", raw)
		}
		fv.SetInt(n)
	case kindFloat64:
		x, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("invalid float %q", raw)
		}
		fv.SetFloat(x)
	case kindDuration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid duration %q", raw)
		}
		fv.SetInt(int64(d))
	case kindStringSlice:
		parts := argvalue.List(raw)
		out := reflect.MakeSlice(fv.Type(), len(parts), len(parts))
		for i, p := range parts {
			out.Index(i).SetString(p)
		}
		fv.Set(out)
	default:
		return fmt.Errorf("unsupported field type %s", fv.Type())
	}
	return nil
}

func classifyKind(t reflect.Type) fieldKind {
	if t.PkgPath() == "time" && t.Name() == "Duration" {
		return kindDuration
	}
	switch t.Kind() {
	case reflect.String:
		return kindString
	case reflect.Bool:
		return kindBool
	case reflect.Int:
		return kindInt
	case reflect.Int64:
		return kindInt64
	case reflect.Float64:
		return kindFloat64
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return kindStringSlice
		}
	case reflect.Map:
		if t.Key().Kind() == reflect.String && t.Elem().Kind() == reflect.String {
			return kindExtraMap
		}
	}
	return kindUnsupported
}

func boolTag(s string) bool {
	if s == "" {
		return false
	}
	v, err := strconv.ParseBool(s)
	return err == nil && v
}

func flattenInputs[T any](in T) (map[string]string, error) {
	var zero T
	t := reflect.TypeOf(zero)
	schema, err := parseInputsSchema(t)
	if err != nil {
		return nil, err
	}
	if t == nil || (t.Kind() == reflect.Struct && t.NumField() == 0) {
		return map[string]string{}, nil
	}
	v := reflect.ValueOf(in)
	out := map[string]string{}
	for _, f := range schema.Fields {
		fv, ok := readFieldByIndex(v, f.fieldIndex)
		if !ok {
			continue
		}
		if f.isExtraBag {
			if fv.IsNil() {
				continue
			}
			iter := fv.MapRange()
			for iter.Next() {
				k := iter.Key().String()
				if _, dup := out[k]; dup {
					continue
				}
				out[k] = iter.Value().String()
			}
			continue
		}
		s, ok := formatField(fv)
		if !ok {
			continue
		}
		out[f.Name] = s
	}
	return out, nil
}

func formatField(fv reflect.Value) (string, bool) {
	switch classifyKind(fv.Type()) {
	case kindString:
		s := fv.String()
		if s == "" {
			return "", false
		}
		return s, true
	case kindBool:
		if !fv.Bool() {
			return "", false
		}
		return "true", true
	case kindInt, kindInt64:
		n := fv.Int()
		if n == 0 {
			return "", false
		}
		return strconv.FormatInt(n, 10), true
	case kindFloat64:
		x := fv.Float()
		if x == 0 {
			return "", false
		}
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case kindDuration:
		d := time.Duration(fv.Int())
		if d == 0 {
			return "", false
		}
		return d.String(), true
	case kindStringSlice:
		if fv.Len() == 0 {
			return "", false
		}
		parts := make([]string, fv.Len())
		for i := range fv.Len() {
			parts[i] = fv.Index(i).String()
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

func hasAnyOtherTag(f reflect.StructField) bool {
	for _, k := range []string{"short", "desc", "default", "required", "enum", "secret"} {
		if _, ok := f.Tag.Lookup(k); ok {
			return true
		}
	}
	return false
}
