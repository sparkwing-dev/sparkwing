package sparkwing

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// ResolveInputs is everything the resolution chain needs to bind a
// pipeline's args. Pulled into a struct because the framework
// populates it from several sources (CLI parser, resolved profile,
// run context) and a struct keeps the call site readable.
type ResolveInputs struct {
	// FlagValues is the parsed CLI flag map keyed by flag name
	// (kebab-cased; matches fieldMeta.Flag). Absence of a key means
	// the user didn't pass --flag on the command line. Values are
	// stored as strings; the resolver converts to the target field's
	// Go type during binding.
	FlagValues map[string]string

	// ProfileName / ProfileIsLocal feed the predicate context used
	// by RequiredWhen and group .When() evaluation. ProfileIsLocal
	// is true when the resolved profile has no controller.
	ProfileName    string
	ProfileIsLocal bool
}

// Resolve binds the schema's fields against the supplied inputs and
// returns a populated reflect.Value of the args struct (kind=Struct,
// type matches s.GoType). Returns an error joining every per-field
// resolution / validation failure so the operator gets a complete
// picture of what's missing or wrong.
//
// Resolution order per field: explicit flag value -> profile
// default-args -> Computed (with already-resolved args) -> Default.
// Validators (OneOf / Min / Max / Custom) fire after all fields
// resolve; RequiredWhen and group constraints fire last because they
// can reference any other field's resolved state.
func (s *Schema) Resolve(in ResolveInputs) (reflect.Value, error) {
	args := reflect.New(s.goType).Elem()
	resolved := make(map[string]any, len(s.fields))

	var problems []error
	for _, fname := range s.order {
		m := s.fields[fname]
		val, ok, err := s.resolveField(m, args, resolved, in)
		if err != nil {
			problems = append(problems, fmt.Errorf("arg %q: %w", m.Flag, err))
			continue
		}
		if ok {
			args.FieldByName(fname).Set(val)
			// Predicate keys are flag names so consumers write
			// ArgEq("target", "prod") -- not the Go field name.
			resolved[m.Flag] = val.Interface()
		}
	}

	if len(problems) > 0 {
		return reflect.Value{}, errors.Join(problems...)
	}

	// All fields bound; now run validation that needs the full
	// resolved set.
	pctx := &resolvedPredCtx{
		values:  resolved,
		profile: in.ProfileName,
		isLocal: in.ProfileIsLocal,
	}

	for _, fname := range s.order {
		m := s.fields[fname]
		val, set := resolved[m.Flag]

		// RequiredWhen / Required check.
		mustHave := m.Required
		if !mustHave && m.RequiredWhen != nil && m.RequiredWhen.Eval(pctx) {
			mustHave = true
		}
		if mustHave && !set {
			reason := "required"
			if m.RequiredWhen != nil {
				reason = "required-when=" + m.RequiredWhen.String()
			}
			problems = append(problems, fmt.Errorf("arg %q: %s, but no value provided", m.Flag, reason))
			continue
		}

		if !set {
			continue
		}

		if err := validateResolvedValue(m, val); err != nil {
			problems = append(problems, fmt.Errorf("arg %q: %w", m.Flag, err))
		}
	}

	// Custom validators run with the typed args struct (after Required
	// gating so they don't see partial state when a required arg is
	// missing).
	if len(problems) == 0 {
		for _, fname := range s.order {
			m := s.fields[fname]
			if !m.HasCustom {
				continue
			}
			out := m.Custom.Call([]reflect.Value{args})
			if !out[0].IsNil() {
				err := out[0].Interface().(error)
				problems = append(problems, fmt.Errorf("arg %q: %w", m.Flag, err))
			}
		}
	}

	// Group constraints run against the final resolved set.
	for _, g := range s.groups {
		// Group fields use Go field names; map them to flag names for
		// the PredicateContext lookup that the group eval performs.
		flagFields := make([]string, len(g.fields))
		for i, gf := range g.fields {
			if fm, ok := s.fields[gf]; ok {
				flagFields[i] = fm.Flag
			} else {
				flagFields[i] = gf
			}
		}
		ge := &groupMeta{kind: g.kind, fields: flagFields, when: g.when, desc: g.desc}
		if err := evalGroup(ge, pctx); err != nil {
			problems = append(problems, err)
		}
	}

	if len(problems) > 0 {
		return reflect.Value{}, errors.Join(problems...)
	}
	return args, nil
}

// resolveField walks the per-source priority order for one field and
// returns (value, set, err). set=false means no source provided a
// value; the caller leaves the struct field at its zero value.
func (s *Schema) resolveField(m *fieldMeta, args reflect.Value, resolved map[string]any, in ResolveInputs) (reflect.Value, bool, error) {
	// 1. Explicit CLI flag wins (post-merge with YAML defaults at the
	//    orchestrator boundary, so this layer sees the union).
	if raw, ok := in.FlagValues[m.Flag]; ok {
		v, err := parseTypedValue(raw, m.GoType)
		if err != nil {
			return reflect.Value{}, false, fmt.Errorf("parse flag value %q: %w", raw, err)
		}
		return v, true, nil
	}
	// 2. Computed (function of already-resolved args).
	if m.HasComputed {
		out := m.Computed.Call([]reflect.Value{args})
		return out[0].Convert(m.GoType), true, nil
	}
	// 3. Literal default.
	if m.HasDefault {
		v, err := coerceToType(m.Default, m.GoType)
		if err != nil {
			return reflect.Value{}, false, fmt.Errorf("schema Default value: %w", err)
		}
		return v, true, nil
	}
	return reflect.Value{}, false, nil
}

// parseTypedValue converts a raw string (from a CLI flag or profile
// default-args) into the supplied Go type. Supports the kinds the
// args system actually exposes -- string, bool, int/uint family,
// float family, and time.Duration. Anything else errors with a
// clear "unsupported type" message.
func parseTypedValue(raw string, t reflect.Type) (reflect.Value, error) {
	if t == reflect.TypeOf(time.Duration(0)) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(d), nil
	}
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf(raw).Convert(t), nil
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(b), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(t).Elem()
		v.SetInt(n)
		return v, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(t).Elem()
		v.SetUint(n)
		return v, nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(t).Elem()
		v.SetFloat(f)
		return v, nil
	}
	return reflect.Value{}, fmt.Errorf("unsupported arg type %s", t.String())
}

// coerceToType converts an any-typed value (most commonly from
// Default) to the target type. Numeric kinds coerce across (so
// Default(3) on an int64 field works); the rest must be directly
// assignable.
func coerceToType(value any, t reflect.Type) (reflect.Value, error) {
	if value == nil {
		return reflect.Zero(t), nil
	}
	v := reflect.ValueOf(value)
	if v.Type() == t || v.Type().AssignableTo(t) {
		return v.Convert(t), nil
	}
	if isNumericKind(v.Kind()) && isNumericKind(t.Kind()) {
		return v.Convert(t), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot coerce %s to %s", v.Type(), t)
}

// validateResolvedValue runs per-field validators (OneOf, Min, Max)
// against a resolved value. Custom validators are NOT run here --
// they execute later with the full typed args struct because they
// often need to compare across fields.
func validateResolvedValue(m *fieldMeta, value any) error {
	if m.HasOneOf {
		matched := false
		for _, allowed := range m.OneOf {
			if predicateValueEqual(value, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("value %v not in OneOf set %v", value, m.OneOf)
		}
	}
	if m.HasMin || m.HasMax {
		if err := checkNumericBounds(value, m.Min, m.Max, m.HasMin, m.HasMax); err != nil {
			return err
		}
	}
	return nil
}

// checkNumericBounds enforces Min/Max against the resolved value.
// Lifts the value to int64 / uint64 / float64 according to its kind
// so cross-kind comparisons (Min(1) on an int32 field) work without
// surprise truncation. Non-numeric kinds shouldn't reach here --
// Schema.Build rejects Min/Max on non-numeric fields.
func checkNumericBounds(value, minV, maxV any, hasMin, hasMax bool) error {
	vRef := reflect.ValueOf(value)
	switch {
	case isIntKind(vRef.Kind()):
		v := vRef.Int()
		if hasMin {
			lo, err := toInt64(minV)
			if err != nil {
				return err
			}
			if v < lo {
				return fmt.Errorf("value %d below Min %d", v, lo)
			}
		}
		if hasMax {
			hi, err := toInt64(maxV)
			if err != nil {
				return err
			}
			if v > hi {
				return fmt.Errorf("value %d above Max %d", v, hi)
			}
		}
	case isUintKind(vRef.Kind()):
		v := vRef.Uint()
		if hasMin {
			lo, err := toUint64(minV)
			if err != nil {
				return err
			}
			if v < lo {
				return fmt.Errorf("value %d below Min %d", v, lo)
			}
		}
		if hasMax {
			hi, err := toUint64(maxV)
			if err != nil {
				return err
			}
			if v > hi {
				return fmt.Errorf("value %d above Max %d", v, hi)
			}
		}
	case isFloatKind(vRef.Kind()):
		v := vRef.Float()
		if hasMin {
			lo, err := toFloat64(minV)
			if err != nil {
				return err
			}
			if v < lo {
				return fmt.Errorf("value %g below Min %g", v, lo)
			}
		}
		if hasMax {
			hi, err := toFloat64(maxV)
			if err != nil {
				return err
			}
			if v > hi {
				return fmt.Errorf("value %g above Max %g", v, hi)
			}
		}
	default:
		return fmt.Errorf("Min/Max not applicable to kind %s", vRef.Kind())
	}
	return nil
}

func toInt64(v any) (int64, error) {
	r := reflect.ValueOf(v)
	switch {
	case isIntKind(r.Kind()):
		return r.Int(), nil
	case isUintKind(r.Kind()):
		return int64(r.Uint()), nil
	case isFloatKind(r.Kind()):
		return int64(r.Float()), nil
	}
	return 0, fmt.Errorf("not numeric: %T", v)
}

func toUint64(v any) (uint64, error) {
	r := reflect.ValueOf(v)
	switch {
	case isUintKind(r.Kind()):
		return r.Uint(), nil
	case isIntKind(r.Kind()):
		n := r.Int()
		if n < 0 {
			return 0, fmt.Errorf("negative bound %d on uint field", n)
		}
		return uint64(n), nil
	}
	return 0, fmt.Errorf("not unsigned-numeric: %T", v)
}

func toFloat64(v any) (float64, error) {
	r := reflect.ValueOf(v)
	switch {
	case isFloatKind(r.Kind()):
		return r.Float(), nil
	case isIntKind(r.Kind()):
		return float64(r.Int()), nil
	case isUintKind(r.Kind()):
		return float64(r.Uint()), nil
	}
	return 0, fmt.Errorf("not numeric: %T", v)
}

// resolvedPredCtx is the PredicateContext implementation used by the
// resolution chain when evaluating RequiredWhen and group .When()
// predicates. Keys are flag names so user-written ArgEq("target",
// "prod") reads naturally regardless of the underlying Go field name.
type resolvedPredCtx struct {
	values  map[string]any
	profile string
	isLocal bool
}

func (c *resolvedPredCtx) Arg(name string) (any, bool) { v, ok := c.values[name]; return v, ok }
func (c *resolvedPredCtx) ProfileName() string         { return c.profile }
func (c *resolvedPredCtx) ProfileIsLocal() bool        { return c.isLocal }

// ResolveAs is the typed convenience wrapper: same semantics as
// Schema.Resolve but returns T directly so callers don't have to
// type-assert the reflect.Value. Errors when the schema's GoType
// doesn't match T.
func ResolveAs[T any](s *Schema, in ResolveInputs) (T, error) {
	var zero T
	want := reflect.TypeOf(zero)
	if s.goType != want {
		return zero, fmt.Errorf("ResolveAs[%s]: schema is for %s", want, s.goType)
	}
	v, err := s.Resolve(in)
	if err != nil {
		return zero, err
	}
	out, ok := v.Interface().(T)
	if !ok {
		return zero, fmt.Errorf("ResolveAs[%s]: type assertion failed", want)
	}
	return out, nil
}

// NewSchemaFromType synthesizes a zero-constraint schema from a
// reflect.Type. Used by the framework when a job declares typed args
// via WithArgs[T] but doesn't implement a Schema() method -- every
// field becomes a plain optional flag with no constraints.
//
// The reflect.Type must be a struct kind; anything else returns an
// error. Behaviorally identical to NewSchema[T]().Build() but
// callable from non-generic framework code.
func NewSchemaFromType(t reflect.Type) (*Schema, error) {
	if t == nil || t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("NewSchemaFromType: type must be a struct; got %v", t)
	}
	s := &Schema{
		goType: t,
		fields: make(map[string]*fieldMeta),
	}
	structFields := reflectStructFields(t)
	for name, sf := range structFields {
		m := &fieldMeta{Name: name, GoType: sf.Type}
		if flag := sf.Tag.Get("flag"); flag != "" {
			m.Flag = flag
		} else {
			m.Flag = kebabCaseFieldName(name)
		}
		m.Desc = sf.Tag.Get("desc")
		s.fields[name] = m
	}
	order, err := topoSortDependencies(s.fields)
	if err != nil {
		return nil, err
	}
	s.order = order
	// Check duplicate flag names (matches SchemaBuilder.Build's check).
	seen := make(map[string]string, len(s.fields))
	for name, m := range s.fields {
		if prior, dup := seen[m.Flag]; dup {
			return nil, fmt.Errorf("NewSchemaFromType: flag --%s declared by both %q and %q", m.Flag, prior, name)
		}
		seen[m.Flag] = name
	}
	return s, nil
}
