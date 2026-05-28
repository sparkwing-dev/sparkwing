package sparkwing

import (
	"fmt"
	"reflect"
)

// Constraint is the closed type for per-field declarations in a
// [SchemaBuilder]. Each constructor below (Required, RequiredWhen,
// Default, Computed, DependsOn, Bind, OneOf, Min, Max, Range,
// Positive, Custom) returns a Constraint that the FieldBuilder
// threads into the field's [fieldMeta]. Constraint construction
// captures the raw inputs (any-typed values, reflect-wrapped
// functions, predicates); type-checking against the args struct's
// field type happens at [SchemaBuilder] build time so the call sites
// stay terse.
//
// The isConstraint marker keeps the interface closed -- arbitrary
// validators belong inside [Custom], not in a parallel Constraint
// implementation.
type Constraint interface {
	applyTo(*fieldMeta) error
	isConstraint()
}

// fieldMeta is the resolved per-field metadata the SchemaBuilder
// produces from chained constraint calls. The resolution chain reads
// it directly -- no map-of-any indirection -- so steps and the
// describe-tree view can branch on typed fields cheaply.
//
// Some fields stay any-typed because the constraint constructors
// can't know the target field's Go type at call time (the user
// writes Default(5) and the framework only learns "field Replicas is
// int" later). SchemaBuilder.Build is the single point where those
// late checks happen.
type fieldMeta struct {
	Name   string       // Go struct field name (also the schema key)
	Flag   string       // CLI flag name (struct tag override or kebab-cased Name)
	Desc   string       // help text from struct tag
	GoType reflect.Type // declared type of the struct field

	Required     bool
	RequiredWhen Predicate
	HasDefault   bool
	Default      any
	HasComputed  bool
	Computed     reflect.Value // func(T) FieldType
	DependsOn    []string
	Bind         string

	// Value validators -- evaluated after resolution against the bound value.
	HasOneOf  bool
	OneOf     []any
	HasMin    bool
	Min       any
	HasMax    bool
	Max       any
	HasCustom bool
	Custom    reflect.Value // func(T) error
}

// --- Required / RequiredWhen ---

type requiredConstraint struct{}

func (requiredConstraint) applyTo(m *fieldMeta) error {
	m.Required = true
	return nil
}
func (requiredConstraint) isConstraint() {}

// Required marks the field as unconditionally required: the resolution
// chain errors if no source (explicit flag, profile default-args,
// Default, or Computed) provides a value. Equivalent to
// RequiredWhen(Always()) but reads more naturally for the common case.
func Required() Constraint { return requiredConstraint{} }

type requiredWhenConstraint struct{ pred Predicate }

func (c requiredWhenConstraint) applyTo(m *fieldMeta) error {
	if m.Required {
		return fmt.Errorf("RequiredWhen conflicts with Required already set on this field")
	}
	if m.RequiredWhen != nil {
		return fmt.Errorf("RequiredWhen already set on this field")
	}
	if c.pred == nil {
		return fmt.Errorf("RequiredWhen called with nil Predicate")
	}
	m.RequiredWhen = c.pred
	return nil
}
func (requiredWhenConstraint) isConstraint() {}

// RequiredWhen marks the field required only when the predicate
// evaluates true at resolution time. Combine with And/Or/Not to
// express conditions like "required when target=prod AND image
// unset"; see [Predicate] for the full vocabulary.
func RequiredWhen(p Predicate) Constraint { return requiredWhenConstraint{pred: p} }

// --- Default / Computed ---

type defaultConstraint struct{ value any }

func (c defaultConstraint) applyTo(m *fieldMeta) error {
	if m.HasDefault {
		return fmt.Errorf("Default already set on this field")
	}
	if m.HasComputed {
		return fmt.Errorf("Default conflicts with Computed already set on this field")
	}
	m.HasDefault = true
	m.Default = c.value
	return nil
}
func (defaultConstraint) isConstraint() {}

// Default supplies a literal fallback used when no higher-priority
// source (explicit flag, profile default-args) provides a value. The
// value's type must match the struct field's type; mismatches are
// caught at schema-build time. Use [Computed] for defaults that
// depend on other args.
func Default(v any) Constraint { return defaultConstraint{value: v} }

type computedConstraint struct{ fn any }

func (c computedConstraint) applyTo(m *fieldMeta) error {
	if m.HasComputed {
		return fmt.Errorf("Computed already set on this field")
	}
	if m.HasDefault {
		return fmt.Errorf("Computed conflicts with Default already set on this field")
	}
	rv := reflect.ValueOf(c.fn)
	if !rv.IsValid() || rv.Kind() != reflect.Func {
		return fmt.Errorf("Computed requires a func; got %T", c.fn)
	}
	t := rv.Type()
	if t.NumIn() != 1 || t.NumOut() != 1 {
		return fmt.Errorf("Computed func must be func(T) FieldType; got %s", t)
	}
	m.HasComputed = true
	m.Computed = rv
	return nil
}
func (computedConstraint) isConstraint() {}

// Computed defines a default that depends on other (already-resolved)
// args. The function shape is func(T) FieldType where T is the args
// struct type and FieldType matches this struct field's type. The
// resolution chain orders evaluation so dependencies bind first;
// cycles among Computed defaults are rejected at schema-build time.
// Pair with [DependsOn] when the dependency isn't visible by static
// inspection (rare; the framework infers it from closure captures
// when possible).
func Computed(fn any) Constraint { return computedConstraint{fn: fn} }

// --- DependsOn / Bind ---

type dependsOnConstraint struct{ names []string }

func (c dependsOnConstraint) applyTo(m *fieldMeta) error {
	if len(c.names) == 0 {
		return fmt.Errorf("DependsOn called with empty name list")
	}
	m.DependsOn = append(m.DependsOn, c.names...)
	return nil
}
func (dependsOnConstraint) isConstraint() {}

// DependsOn declares an explicit ordering edge: the framework
// resolves the named args before this one. Use when a Custom or
// RequiredWhen reads another arg's value without the framework being
// able to infer the edge from a Computed closure.
func DependsOn(names ...string) Constraint { return dependsOnConstraint{names: names} }

type bindConstraint struct{ argName string }

func (c bindConstraint) applyTo(m *fieldMeta) error {
	if c.argName == "" {
		return fmt.Errorf("Bind requires a non-empty arg name")
	}
	if m.Bind != "" {
		return fmt.Errorf("Bind already set on this field (%q)", m.Bind)
	}
	m.Bind = c.argName
	return nil
}
func (bindConstraint) isConstraint() {}

// Bind ties this struct field to a schema-bearing YAML arg key. The
// v0.6.0 vocabulary is "target" -- declaring Bind("target") tells the
// framework "this string field IS the value of args.target in the
// pipeline's YAML; resolving it triggers the YAML's runners/source/
// secrets binding for that target." Other bind names are reserved
// for future schema-bearing kinds; passing an unknown name errors at
// schema-build time.
func Bind(argName string) Constraint { return bindConstraint{argName: argName} }

// --- Value validators: OneOf / Min / Max / Range / Positive / Custom ---

type oneOfConstraint struct{ values []any }

func (c oneOfConstraint) applyTo(m *fieldMeta) error {
	if len(c.values) == 0 {
		return fmt.Errorf("OneOf requires at least one allowed value")
	}
	if m.HasOneOf {
		return fmt.Errorf("OneOf already set on this field")
	}
	m.HasOneOf = true
	m.OneOf = c.values
	return nil
}
func (oneOfConstraint) isConstraint() {}

// OneOf restricts the field's resolved value to the supplied set.
// Errors at resolution time when the value matches none of them.
// Values are compared with the same predicate-equality rules as
// [ArgEq] (numeric coercion across int/uint kinds).
func OneOf(values ...any) Constraint { return oneOfConstraint{values: values} }

type minConstraint struct{ v any }

func (c minConstraint) applyTo(m *fieldMeta) error {
	if m.HasMin {
		return fmt.Errorf("Min already set on this field")
	}
	m.HasMin = true
	m.Min = c.v
	return nil
}
func (minConstraint) isConstraint() {}

// Min sets a lower bound on the field's resolved value. Applies to
// numeric kinds (int family, uint family, float family); the schema
// builder rejects Min on non-numeric fields at build time.
func Min(v any) Constraint { return minConstraint{v: v} }

type maxConstraint struct{ v any }

func (c maxConstraint) applyTo(m *fieldMeta) error {
	if m.HasMax {
		return fmt.Errorf("Max already set on this field")
	}
	m.HasMax = true
	m.Max = c.v
	return nil
}
func (maxConstraint) isConstraint() {}

// Max sets an upper bound; mirror of [Min].
func Max(v any) Constraint { return maxConstraint{v: v} }

type rangeConstraint struct{ min, max any }

func (c rangeConstraint) applyTo(m *fieldMeta) error {
	if err := (minConstraint{v: c.min}).applyTo(m); err != nil {
		return err
	}
	return (maxConstraint{v: c.max}).applyTo(m)
}
func (rangeConstraint) isConstraint() {}

// Range is sugar for Min(min)+Max(max). Equivalent to chaining the
// two calls; provided because "between N and M" reads as a single
// concept.
func Range(min, max any) Constraint { return rangeConstraint{min: min, max: max} }

type positiveConstraint struct{}

func (positiveConstraint) applyTo(m *fieldMeta) error {
	return (minConstraint{v: 1}).applyTo(m)
}
func (positiveConstraint) isConstraint() {}

// Positive is sugar for Min(1). Common enough for replica counts,
// retry limits, and pool sizes to earn a dedicated constructor.
func Positive() Constraint { return positiveConstraint{} }

type customConstraint struct{ fn any }

func (c customConstraint) applyTo(m *fieldMeta) error {
	if m.HasCustom {
		return fmt.Errorf("Custom already set on this field")
	}
	rv := reflect.ValueOf(c.fn)
	if !rv.IsValid() || rv.Kind() != reflect.Func {
		return fmt.Errorf("Custom requires a func; got %T", c.fn)
	}
	t := rv.Type()
	if t.NumIn() != 1 || t.NumOut() != 1 {
		return fmt.Errorf("Custom func must be func(T) error; got %s", t)
	}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	if !t.Out(0).Implements(errType) && t.Out(0) != errType {
		return fmt.Errorf("Custom func must return error; got %s", t.Out(0))
	}
	m.HasCustom = true
	m.Custom = rv
	return nil
}
func (customConstraint) isConstraint() {}

// Custom is the escape hatch for validators that don't fit the
// declarative vocabulary. The function shape is func(T) error where
// T is the args struct; return non-nil to reject the resolved args
// with the returned error message. Use sparingly -- the declarative
// constraints surface in `pipeline describe --args` and the dashboard
// while Custom validators stay opaque.
func Custom(fn any) Constraint { return customConstraint{fn: fn} }
