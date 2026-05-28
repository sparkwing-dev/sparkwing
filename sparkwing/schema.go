package sparkwing

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Schema is the immutable, fully-validated arg metadata produced by
// SchemaBuilder.Build. The resolution chain consumes this directly:
// fields are pre-ordered for evaluation (topo sort over DependsOn +
// inferred Computed edges), groups are validated to reference real
// fields, and every Default / Computed / Custom signature has been
// type-checked against the args struct.
//
// Schemas are safe to share across runs of the same job -- they
// carry no per-run state. The resolution chain copies the relevant
// metadata when it needs a working set.
type Schema struct {
	goType reflect.Type
	fields map[string]*fieldMeta // by Go field name
	groups []*groupMeta
	order  []string // topo-sorted field names: resolve in this order
}

// GoType returns the args struct type the schema validates against.
// Useful for the CLI flag registrar and the describe-tree renderer.
func (s *Schema) GoType() reflect.Type { return s.goType }

// Fields returns the field names in resolution order (topo-sorted
// over DependsOn + inferred Computed edges). Useful for tooling that
// wants to iterate args in a deterministic order.
func (s *Schema) Fields() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// Field looks up the per-field metadata bundle. The bundle is
// internal; this accessor is here for the resolution chain and
// tests, not the public surface.
func (s *Schema) field(name string) *fieldMeta { return s.fields[name] }

// Groups returns the group metadata bundles. Internal accessor for
// the resolution chain.
func (s *Schema) groupMetas() []*groupMeta { return s.groups }

// SchemaBuilder is the chainable builder for a job's args schema.
// The type parameter T binds to the args struct; Field(name) returns
// a FieldBuilder[T] that accumulates per-field constraints; Group
// declares cross-field cardinality rules; Build validates everything
// and produces an immutable [Schema].
//
// Errors from constraint application accumulate on the builder so
// chains stay terse; Build returns the joined error if anything went
// wrong during chained calls.
type SchemaBuilder[T any] struct {
	goType reflect.Type
	fields map[string]*FieldBuilder[T]
	groups []*GroupBuilder
	errs   []error
}

// NewSchema constructs a fresh SchemaBuilder over the args struct T.
// The framework constructs one per job at registration time and
// passes it to the job's Schema(*SchemaBuilder[T]) method.
func NewSchema[T any]() *SchemaBuilder[T] {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		// T is an interface type; should never happen for a real Args
		// struct, but guard so callers get a clear error rather than
		// a nil-pointer panic later.
		panic("sparkwing.NewSchema: T must be a concrete struct type (got nil reflect.Type)")
	}
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("sparkwing.NewSchema: T must be a struct; got %s", t.Kind()))
	}
	return &SchemaBuilder[T]{
		goType: t,
		fields: make(map[string]*FieldBuilder[T]),
	}
}

// Field returns the FieldBuilder for the named struct field. The
// same FieldBuilder is returned on repeated calls so chained calls
// across multiple statements accumulate constraints on a single
// metadata bundle.
//
// The field must exist on T; references to nonexistent fields are
// caught at Build time (not here) so callers see a single grouped
// error instead of a panic mid-chain.
func (sb *SchemaBuilder[T]) Field(name string) *FieldBuilder[T] {
	if fb, ok := sb.fields[name]; ok {
		return fb
	}
	fb := &FieldBuilder[T]{
		sb:   sb,
		meta: &fieldMeta{Name: name},
	}
	sb.fields[name] = fb
	return fb
}

// Group declares a cross-field cardinality rule over the named
// struct fields. The returned GroupBuilder takes the kind via
// ExactlyOne / AtLeastOne / AtMostOne / AllOrNone, plus optional
// When and Desc decorators.
func (sb *SchemaBuilder[T]) Group(names ...string) *GroupBuilder {
	g := newGroupBuilder(names)
	sb.groups = append(sb.groups, g)
	return g
}

// Build validates the accumulated schema against the args struct T
// and produces an immutable [Schema] ready for the resolution chain.
// Returns the joined error of every validation failure so authors
// get a complete picture in one pass.
func (sb *SchemaBuilder[T]) Build() (*Schema, error) {
	s := &Schema{
		goType: sb.goType,
		fields: make(map[string]*fieldMeta, sb.goType.NumField()),
	}
	var problems []error
	problems = append(problems, sb.errs...)

	structFields := reflectStructFields(sb.goType)

	// 1. Every key in sb.fields must refer to a real struct field.
	for name := range sb.fields {
		if _, ok := structFields[name]; !ok {
			problems = append(problems, fmt.Errorf(
				"Schema for %s: field %q does not exist on struct",
				sb.goType.Name(), name,
			))
		}
	}

	// 2. Build a fieldMeta for every struct field. Apply tag-derived
	//    Flag and Desc; overlay any constraint bundle from sb.fields.
	for name, sf := range structFields {
		var m *fieldMeta
		if fb, ok := sb.fields[name]; ok {
			m = fb.meta
		} else {
			m = &fieldMeta{Name: name}
		}
		m.GoType = sf.Type
		if flag := sf.Tag.Get("flag"); flag != "" {
			m.Flag = flag
		} else {
			m.Flag = kebabCaseFieldName(name)
		}
		m.Desc = sf.Tag.Get("desc")
		s.fields[name] = m
	}

	// 3. Per-field constraint validation (types, signatures, etc).
	for name, m := range s.fields {
		if err := validateFieldMeta(m, sb.goType); err != nil {
			problems = append(problems, fmt.Errorf("Schema field %q: %w", name, err))
		}
	}

	// 4. DependsOn references must point at real fields, and the
	//    inferred dependency DAG (DependsOn edges + Computed-inferred
	//    edges from closure captures we can't introspect, so we trust
	//    the explicit DependsOn declaration) must be acyclic.
	for name, m := range s.fields {
		for _, dep := range m.DependsOn {
			if _, ok := s.fields[dep]; !ok {
				problems = append(problems, fmt.Errorf(
					"Schema field %q: DependsOn(%q) refers to nonexistent field",
					name, dep,
				))
			}
		}
	}
	order, cycleErr := topoSortDependencies(s.fields)
	if cycleErr != nil {
		problems = append(problems, cycleErr)
	}
	s.order = order

	// 5. Flag names must be unique.
	seenFlag := make(map[string]string, len(s.fields))
	for name, m := range s.fields {
		if m.Flag == "" {
			continue
		}
		if prior, ok := seenFlag[m.Flag]; ok {
			problems = append(problems, fmt.Errorf(
				"Schema for %s: flag --%s declared by both %q and %q",
				sb.goType.Name(), m.Flag, prior, name,
			))
			continue
		}
		seenFlag[m.Flag] = name
	}

	// 6. Group kinds must be set, and group fields must exist.
	for _, g := range sb.groups {
		if g.meta.kind == groupKindUnset {
			problems = append(problems, fmt.Errorf(
				"Schema for %s: group [%s] has no cardinality set (call ExactlyOne/AtLeastOne/AtMostOne/AllOrNone)",
				sb.goType.Name(), strings.Join(g.meta.fields, ","),
			))
		}
		for _, fname := range g.meta.fields {
			if _, ok := structFields[fname]; !ok {
				problems = append(problems, fmt.Errorf(
					"Schema for %s: group references nonexistent field %q",
					sb.goType.Name(), fname,
				))
			}
		}
		s.groups = append(s.groups, g.meta)
	}

	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	return s, nil
}

// FieldBuilder is the chainable handle returned by SchemaBuilder.Field.
// Each method below is a thin wrapper that applies the matching
// [Constraint] to the field's metadata. Errors during application
// accumulate on the parent SchemaBuilder and surface from Build.
type FieldBuilder[T any] struct {
	sb   *SchemaBuilder[T]
	meta *fieldMeta
}

func (fb *FieldBuilder[T]) apply(c Constraint) *FieldBuilder[T] {
	if err := c.applyTo(fb.meta); err != nil {
		fb.sb.errs = append(fb.sb.errs, fmt.Errorf("Field %q: %w", fb.meta.Name, err))
	}
	return fb
}

// Required marks the field unconditionally required.
func (fb *FieldBuilder[T]) Required() *FieldBuilder[T] { return fb.apply(Required()) }

// RequiredWhen marks the field required when the predicate holds.
func (fb *FieldBuilder[T]) RequiredWhen(p Predicate) *FieldBuilder[T] {
	return fb.apply(RequiredWhen(p))
}

// Default supplies a literal fallback value.
func (fb *FieldBuilder[T]) Default(v any) *FieldBuilder[T] { return fb.apply(Default(v)) }

// Computed supplies a function-based default that may depend on
// other (already-resolved) args.
func (fb *FieldBuilder[T]) Computed(fn any) *FieldBuilder[T] { return fb.apply(Computed(fn)) }

// DependsOn declares ordering edges to upstream args.
func (fb *FieldBuilder[T]) DependsOn(names ...string) *FieldBuilder[T] {
	return fb.apply(DependsOn(names...))
}

// Bind ties this field to a schema-bearing YAML arg key.
func (fb *FieldBuilder[T]) Bind(argName string) *FieldBuilder[T] { return fb.apply(Bind(argName)) }

// OneOf restricts the resolved value to the supplied set.
func (fb *FieldBuilder[T]) OneOf(values ...any) *FieldBuilder[T] { return fb.apply(OneOf(values...)) }

// Min sets a numeric lower bound.
func (fb *FieldBuilder[T]) Min(v any) *FieldBuilder[T] { return fb.apply(Min(v)) }

// Max sets a numeric upper bound.
func (fb *FieldBuilder[T]) Max(v any) *FieldBuilder[T] { return fb.apply(Max(v)) }

// Range is sugar for Min(min)+Max(max).
func (fb *FieldBuilder[T]) Range(min, max any) *FieldBuilder[T] { return fb.apply(Range(min, max)) }

// Positive is sugar for Min(1).
func (fb *FieldBuilder[T]) Positive() *FieldBuilder[T] { return fb.apply(Positive()) }

// Custom is the escape-hatch validator (func(T) error).
func (fb *FieldBuilder[T]) Custom(fn any) *FieldBuilder[T] { return fb.apply(Custom(fn)) }

// --- helpers ---

// reflectStructFields returns the exported fields of a struct type
// keyed by Go field name. Unexported fields are skipped (they can't
// be populated via reflection from CLI flags anyway). Anonymous
// embedded fields are skipped too -- args structs are flat by design.
func reflectStructFields(t reflect.Type) map[string]reflect.StructField {
	out := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		if sf.Anonymous {
			continue
		}
		out[sf.Name] = sf
	}
	return out
}

// kebabCaseFieldName converts a CamelCase Go field name to a
// kebab-cased CLI flag (Replicas -> replicas, PoolSize -> pool-size,
// SlackWebhook -> slack-webhook). Handles consecutive uppercase
// (URLToFetch -> url-to-fetch) by inserting separators before the
// last uppercase in a run.
func kebabCaseFieldName(name string) string {
	if name == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range name {
		isUpper := r >= 'A' && r <= 'Z'
		if i > 0 && isUpper {
			prev := rune(name[i-1])
			prevUpper := prev >= 'A' && prev <= 'Z'
			// Break on Aa boundary, OR on AAa boundary (the last A starts a new word).
			nextLower := false
			if i+1 < len(name) {
				next := rune(name[i+1])
				nextLower = next >= 'a' && next <= 'z'
			}
			if !prevUpper || (prevUpper && nextLower) {
				b.WriteByte('-')
			}
		}
		if isUpper {
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validateFieldMeta cross-checks per-field constraint values against
// the field's Go type. Called once per field from Build after the
// fieldMeta has been populated with the struct field's reflect type.
func validateFieldMeta(m *fieldMeta, argsType reflect.Type) error {
	var problems []error

	if m.Bind != "" && m.Bind != "target" {
		problems = append(problems, fmt.Errorf("Bind(%q): only \"target\" is supported in v0.6; future bind names will land as schema-bearing args are added", m.Bind))
	}

	if m.HasDefault {
		if err := checkAssignable(m.Default, m.GoType, "Default"); err != nil {
			problems = append(problems, err)
		}
	}

	if m.HasComputed {
		t := m.Computed.Type()
		if t.NumIn() != 1 || t.In(0) != argsType {
			problems = append(problems, fmt.Errorf(
				"Computed func: expected func(%s) %s; got %s",
				argsType.Name(), m.GoType.String(), t.String(),
			))
		} else if t.NumOut() != 1 || !t.Out(0).AssignableTo(m.GoType) {
			problems = append(problems, fmt.Errorf(
				"Computed func: expected return type assignable to %s; got %s",
				m.GoType.String(), t.Out(0).String(),
			))
		}
	}

	if m.HasCustom {
		t := m.Custom.Type()
		if t.NumIn() != 1 || t.In(0) != argsType {
			problems = append(problems, fmt.Errorf(
				"Custom func: expected func(%s) error; got %s",
				argsType.Name(), t.String(),
			))
		}
	}

	if (m.HasMin || m.HasMax) && !isNumericKind(m.GoType.Kind()) {
		problems = append(problems, fmt.Errorf(
			"Min/Max applies only to numeric kinds; field type is %s",
			m.GoType.String(),
		))
	}

	if m.HasOneOf {
		for i, v := range m.OneOf {
			if err := checkAssignable(v, m.GoType, fmt.Sprintf("OneOf[%d]", i)); err != nil {
				problems = append(problems, err)
			}
		}
	}

	if len(problems) > 0 {
		return errors.Join(problems...)
	}
	return nil
}

// checkAssignable verifies that value's runtime type is assignable
// to target. Used by Default and OneOf to surface type mismatches at
// schema-build time rather than at resolution time.
func checkAssignable(value any, target reflect.Type, label string) error {
	if value == nil {
		return nil
	}
	vt := reflect.TypeOf(value)
	if vt.AssignableTo(target) {
		return nil
	}
	if isNumericKind(vt.Kind()) && isNumericKind(target.Kind()) {
		return nil
	}
	return fmt.Errorf("%s: value of type %s not assignable to field type %s",
		label, vt.String(), target.String())
}

func isNumericKind(k reflect.Kind) bool {
	return isIntKind(k) || isUintKind(k) || isFloatKind(k)
}

// topoSortDependencies returns the field names in evaluation order
// (every field's DependsOn entries come earlier in the slice).
// Detects cycles and returns a descriptive error pointing at the
// involved field names when one exists.
func topoSortDependencies(fields map[string]*fieldMeta) ([]string, error) {
	names := make([]string, 0, len(fields))
	for n := range fields {
		names = append(names, n)
	}
	sort.Strings(names) // stable iteration

	indeg := make(map[string]int, len(names))
	for _, n := range names {
		indeg[n] = 0
	}
	deps := make(map[string][]string, len(names))
	for _, n := range names {
		m := fields[n]
		// Only known fields contribute edges; missing-field errors
		// are reported separately by the caller.
		for _, d := range m.DependsOn {
			if _, ok := fields[d]; !ok {
				continue
			}
			deps[d] = append(deps[d], n)
			indeg[n]++
		}
	}

	queue := make([]string, 0, len(names))
	for _, n := range names {
		if indeg[n] == 0 {
			queue = append(queue, n)
		}
	}

	var out []string
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		out = append(out, head)
		for _, child := range deps[head] {
			indeg[child]--
			if indeg[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if len(out) != len(names) {
		// Surface the remaining (still-cyclic) nodes.
		remaining := make([]string, 0, len(names)-len(out))
		for _, n := range names {
			if indeg[n] > 0 {
				remaining = append(remaining, n)
			}
		}
		return nil, fmt.Errorf("Schema: dependency cycle among fields [%s]", strings.Join(remaining, ", "))
	}
	return out, nil
}
