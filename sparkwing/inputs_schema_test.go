package sparkwing

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- Schema parsing: type recognition ---

type allTypesInputs struct {
	S    string            `flag:"s"`
	B    bool              `flag:"b"`
	I    int               `flag:"i"`
	I64  int64             `flag:"i64"`
	F    float64           `flag:"f"`
	D    time.Duration     `flag:"d"`
	Strs []string          `flag:"strs"`
	Bag  map[string]string `flag:",extra"`
}

func TestParseInputsSchema_AllTypes(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(allTypesInputs{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !schema.Extra {
		t.Errorf("Extra should be true (bag field present)")
	}
	want := map[string]string{
		"s":    "string",
		"b":    "bool",
		"i":    "int",
		"i64":  "int64",
		"f":    "float64",
		"d":    "duration",
		"strs": "[]string",
	}
	got := map[string]string{}
	for _, f := range schema.Fields {
		if f.isExtraBag {
			continue
		}
		got[f.Name] = f.Type
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("type mapping mismatch:\nwant %v\ngot  %v", want, got)
	}
}

// --- Schema parsing: registration-time validation rules ---

type requiredWithDefault struct {
	X string `flag:"x" required:"true" default:"foo"`
}

func TestParseInputsSchema_RequiredAndDefaultMutex(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(requiredWithDefault{}))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected required+default mutex error, got %v", err)
	}
}

type enumWithoutDefaultOrRequired struct {
	X string `flag:"x" enum:"a,b,c"`
}

func TestParseInputsSchema_EnumNeedsDefaultOrRequired(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(enumWithoutDefaultOrRequired{}))
	if err == nil || !strings.Contains(err.Error(), "enum requires") {
		t.Fatalf("expected enum-without-anchor error, got %v", err)
	}
}

type enumOnInt struct {
	X int `flag:"x" enum:"1,2,3" required:"true"`
}

func TestParseInputsSchema_EnumOnNonStringErrors(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(enumOnInt{}))
	if err == nil || !strings.Contains(err.Error(), "enum requires a string") {
		t.Fatalf("expected enum-on-non-string error, got %v", err)
	}
}

type enumDefaultOutOfSet struct {
	X string `flag:"x" enum:"a,b,c" default:"d"`
}

func TestParseInputsSchema_EnumDefaultMustBeInSet(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(enumDefaultOutOfSet{}))
	if err == nil || !strings.Contains(err.Error(), "not in enum") {
		t.Fatalf("expected default-not-in-enum error, got %v", err)
	}
}

type shortCollision struct {
	X string `flag:"xray" short:"x"`
	Y string `flag:"yankee" short:"x"`
}

func TestParseInputsSchema_ShortCollisionErrors(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(shortCollision{}))
	if err == nil || !strings.Contains(err.Error(), "short") {
		t.Fatalf("expected short-collision error, got %v", err)
	}
}

type shortMultiChar struct {
	X string `flag:"x" short:"xx"`
}

func TestParseInputsSchema_ShortMustBeOneChar(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(shortMultiChar{}))
	if err == nil || !strings.Contains(err.Error(), "single ASCII") {
		t.Fatalf("expected short-multi-char error, got %v", err)
	}
}

type extraWithOtherTags struct {
	X map[string]string `flag:",extra" desc:"forbidden"`
}

func TestParseInputsSchema_ExtraCantCombineWithOtherTags(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(extraWithOtherTags{}))
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("expected extra+other-tags error, got %v", err)
	}
}

type extraNotAMap struct {
	X string `flag:",extra"`
}

func TestParseInputsSchema_ExtraNeedsMapType(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(extraNotAMap{}))
	if err == nil || !strings.Contains(err.Error(), "map[string]string") {
		t.Fatalf("expected extra-needs-map error, got %v", err)
	}
}

type doubleExtra struct {
	A map[string]string `flag:",extra"`
	B map[string]string `flag:",extra"`
}

func TestParseInputsSchema_AtMostOneExtra(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(doubleExtra{}))
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("expected double-extra error, got %v", err)
	}
}

type duplicateName struct {
	A string `flag:"x"`
	B string `flag:"x"`
}

func TestParseInputsSchema_DuplicateFlagName(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(duplicateName{}))
	if err == nil || !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}
}

type unsupportedFieldType struct {
	X complex64 `flag:"x"`
}

func TestParseInputsSchema_UnsupportedTypeRejected(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(unsupportedFieldType{}))
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("expected unsupported-type error, got %v", err)
	}
}

type mapNotExtra struct {
	X map[string]string `flag:"x"`
}

func TestParseInputsSchema_MapWithoutExtraRejected(t *testing.T) {
	_, err := parseInputsSchema(reflect.TypeOf(mapNotExtra{}))
	if err == nil || !strings.Contains(err.Error(), "supported with flag:\",extra\"") {
		t.Fatalf("expected map-needs-extra error, got %v", err)
	}
}

// Fields without a `flag` tag are silently skipped (treated as
// internal helpers, not flags). Verifies the parser doesn't trip on
// state-keeping fields someone might add to their Inputs struct.
type withUntaggedField struct {
	Tagged   string `flag:"x"`
	internal int    //nolint:unused
}

func TestParseInputsSchema_SkipsUntagged(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(withUntaggedField{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(schema.Fields) != 1 {
		t.Fatalf("expected 1 field (untagged skipped), got %d", len(schema.Fields))
	}
	if schema.Fields[0].Name != "x" {
		t.Errorf("got field %q, want x", schema.Fields[0].Name)
	}
}

// --- populateInputs: parsing all types from the wire-format map ---

type populateAllInputs struct {
	S    string            `flag:"s"`
	B    bool              `flag:"b"`
	I    int               `flag:"i"`
	I64  int64             `flag:"i64"`
	F    float64           `flag:"f"`
	D    time.Duration     `flag:"d"`
	Strs []string          `flag:"strs"`
	Bag  map[string]string `flag:",extra"`
}

func TestPopulateInputs_AllTypes(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(populateAllInputs{}))
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var in populateAllInputs
	err = populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{
		"s":       "hello",
		"b":       "true",
		"i":       "42",
		"i64":     "9999999999",
		"f":       "3.14",
		"d":       "1h30m",
		"strs":    "a,b,c",
		"unknown": "stashed",
	})
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if in.S != "hello" {
		t.Errorf("S=%q", in.S)
	}
	if !in.B {
		t.Errorf("B=false")
	}
	if in.I != 42 {
		t.Errorf("I=%d", in.I)
	}
	if in.I64 != 9_999_999_999 {
		t.Errorf("I64=%d", in.I64)
	}
	if in.F != 3.14 {
		t.Errorf("F=%v", in.F)
	}
	if in.D != 90*time.Minute {
		t.Errorf("D=%v", in.D)
	}
	if !reflect.DeepEqual(in.Strs, []string{"a", "b", "c"}) {
		t.Errorf("Strs=%v", in.Strs)
	}
	if in.Bag["unknown"] != "stashed" {
		t.Errorf("Bag missing unknown: %v", in.Bag)
	}
}

func TestPopulateInputs_DefaultsApplied(t *testing.T) {
	type withDefaults struct {
		Target string `flag:"target" default:"local"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(withDefaults{}))
	var in withDefaults
	if err := populateInputs(schema, reflect.ValueOf(&in).Elem(), nil); err != nil {
		t.Fatalf("populate: %v", err)
	}
	if in.Target != "local" {
		t.Errorf("Target=%q, want local", in.Target)
	}
}

func TestPopulateInputs_RequiredMissingErrors(t *testing.T) {
	type req struct {
		Repo string `flag:"repo" required:"true"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(req{}))
	var in req
	err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("expected required-missing error, got %v", err)
	}
}

func TestPopulateInputs_UnknownFlagErrorsByDefault(t *testing.T) {
	type plain struct {
		X string `flag:"x"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(plain{}))
	var in plain
	err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{
		"x":       "set",
		"strange": "denied",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown-flag error, got %v", err)
	}
}

func TestPopulateInputs_BagFieldAcceptsUnknown(t *testing.T) {
	type bagged struct {
		X     string            `flag:"x"`
		Extra map[string]string `flag:",extra"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(bagged{}))
	var in bagged
	err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{
		"x":   "primary",
		"foo": "1",
		"bar": "2",
	})
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if in.X != "primary" {
		t.Errorf("X=%q", in.X)
	}
	if in.Extra["foo"] != "1" || in.Extra["bar"] != "2" {
		t.Errorf("Extra missing entries: %v", in.Extra)
	}
	if _, present := in.Extra["x"]; present {
		t.Errorf("declared flag %q should not appear in the bag: %v", "x", in.Extra)
	}
}

func TestPopulateInputs_EnumValidationAtParseTime(t *testing.T) {
	type enumed struct {
		Target string `flag:"target" enum:"local,staging,prod" default:"local"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(enumed{}))
	var in enumed
	err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{
		"target": "banana",
	})
	if err == nil || !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("expected enum violation error, got %v", err)
	}
	// Allowed values pass and propagate.
	in = enumed{}
	if err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{"target": "prod"}); err != nil {
		t.Fatalf("expected prod to be accepted, got %v", err)
	}
	if in.Target != "prod" {
		t.Errorf("Target=%q", in.Target)
	}
}

func TestPopulateInputs_BadIntErrors(t *testing.T) {
	type intInputs struct {
		Count int `flag:"count"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(intInputs{}))
	var in intInputs
	err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{"count": "abc"})
	if err == nil || !strings.Contains(err.Error(), "invalid int") {
		t.Fatalf("expected invalid-int error, got %v", err)
	}
}

func TestPopulateInputs_BadDurationErrors(t *testing.T) {
	type d struct {
		T time.Duration `flag:"t"`
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(d{}))
	var in d
	err := populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{"t": "30 days"})
	if err == nil || !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("expected invalid-duration error, got %v", err)
	}
}

// --- flattenInputs: round-trip through wire format ---

type roundTripInputs struct {
	S    string            `flag:"s"`
	B    bool              `flag:"b"`
	I    int               `flag:"i"`
	D    time.Duration     `flag:"d"`
	Strs []string          `flag:"strs"`
	Bag  map[string]string `flag:",extra"`
}

func TestFlattenInputs_RoundTrip(t *testing.T) {
	original := roundTripInputs{
		S:    "hello",
		B:    true,
		I:    7,
		D:    2 * time.Hour,
		Strs: []string{"x", "y"},
		Bag:  map[string]string{"extra": "val"},
	}
	flat, err := flattenInputs(original)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(roundTripInputs{}))
	var back roundTripInputs
	if err := populateInputs(schema, reflect.ValueOf(&back).Elem(), flat); err != nil {
		t.Fatalf("repopulate: %v", err)
	}
	// Bag preservation only requires the extra entries to round-trip;
	// the bag itself reflects the inverse-projection map identity.
	if back.S != original.S || back.B != original.B || back.I != original.I || back.D != original.D {
		t.Errorf("scalar round-trip mismatch: original=%+v back=%+v", original, back)
	}
	if !reflect.DeepEqual(back.Strs, original.Strs) {
		t.Errorf("slice round-trip mismatch: original=%v back=%v", original.Strs, back.Strs)
	}
	if back.Bag["extra"] != "val" {
		t.Errorf("bag round-trip lost entry: %v", back.Bag)
	}
}

// --- Pipeline registration end-to-end: the user-visible path ---

type secretInputs struct {
	Token string `flag:"token" secret:"true"`
}

type secretPipe struct{ captured secretInputs }

func (sp *secretPipe) Plan(_ context.Context, plan *Plan, in secretInputs, rc RunContext) error {
	sp.captured = in
	Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

// secretValuesCreds is at package scope so the Pipeline[T] inferred
// type and the Plan signature reference the same named struct.
type secretValuesCreds struct {
	Token   string `flag:"token" secret:"true"`
	Backup  string `flag:"backup" secret:"true" default:"fallback-secret"`
	Visible string `flag:"visible"`
	Empty   string `flag:"empty" secret:"true"`
}

type secretValuesPipe struct{}

func (secretValuesPipe) Plan(_ context.Context, _ *Plan, _ secretValuesCreds, _ RunContext) error {
	return nil
}

// SecretValues is what the orchestrator pulls into the run's Masker.
// Verifies passed values, defaulted values, and that empty/non-secret
// fields are excluded.
func TestRegistration_SecretValues(t *testing.T) {
	Register[secretValuesCreds]("secret-values-fixture", func() Pipeline[secretValuesCreds] {
		return secretValuesPipe{}
	})
	reg, _ := Lookup("secret-values-fixture")
	got := reg.SecretValues(map[string]string{
		"token":   "from-args",
		"visible": "not-secret",
		// Backup unset → default applies; Empty stays "" → skipped.
	})
	want := map[string]bool{"from-args": true, "fallback-secret": true}
	if len(got) != len(want) {
		t.Fatalf("got %v secret values, want 2: %v", len(got), got)
	}
	for _, v := range got {
		if !want[v] {
			t.Errorf("unexpected secret value %q (want %v)", v, want)
		}
	}
}

func TestRegister_SchemaCarriesSecretBit(t *testing.T) {
	captured := &secretPipe{}
	Register[secretInputs]("secret-bit-fixture", func() Pipeline[secretInputs] { return captured })
	reg, ok := Lookup("secret-bit-fixture")
	if !ok {
		t.Fatal("not registered")
	}
	if len(reg.Schema.Fields) != 1 {
		t.Fatalf("schema fields=%d", len(reg.Schema.Fields))
	}
	if !reg.Schema.Fields[0].Secret {
		t.Errorf("secret bit lost in schema: %+v", reg.Schema.Fields[0])
	}
}

// --- Anonymous embedded structs ---
//
// Pipelines that share a flag bundle via embedding should see the
// embedded struct's flags surface as first-class CLI flags. The
// schema walker must recurse into anonymous embedded fields, the
// populator must reach the leaf field through the embed, and the
// flattener must read it back the same way.

type embeddedSkipFilter struct {
	Skip string `flag:"skip" desc:"comma-separated job names to skip"`
	Only string `flag:"only" desc:"comma-separated job names to run exclusively"`
}

type embedOuter struct {
	Version string `flag:"version" desc:"release tag"`
	embeddedSkipFilter
}

func TestParseInputsSchema_RecursesAnonymousEmbed(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(embedOuter{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]string{}
	for _, f := range schema.Fields {
		got[f.Name] = f.Type
	}
	want := map[string]string{
		"version": "string",
		"skip":    "string",
		"only":    "string",
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("embedded flags missing:\nwant %v\ngot  %v", want, got)
	}
}

func TestPopulateInputs_AnonymousEmbed(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(embedOuter{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var in embedOuter
	err = populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{
		"version": "v1.2.3",
		"skip":    "lint",
		"only":    "build",
	})
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if in.Version != "v1.2.3" {
		t.Errorf("Version=%q", in.Version)
	}
	if in.Skip != "lint" {
		t.Errorf("Skip=%q", in.Skip)
	}
	if in.Only != "build" {
		t.Errorf("Only=%q", in.Only)
	}
}

func TestFlattenInputs_AnonymousEmbedRoundTrip(t *testing.T) {
	in := embedOuter{
		Version: "v9",
		embeddedSkipFilter: embeddedSkipFilter{
			Skip: "a,b",
			Only: "c",
		},
	}
	flat, err := flattenInputs(in)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	if flat["version"] != "v9" || flat["skip"] != "a,b" || flat["only"] != "c" {
		t.Errorf("flattened embed lost values: %v", flat)
	}
	schema, _ := parseInputsSchema(reflect.TypeOf(embedOuter{}))
	var back embedOuter
	if err := populateInputs(schema, reflect.ValueOf(&back).Elem(), flat); err != nil {
		t.Fatalf("repopulate: %v", err)
	}
	if back != in {
		t.Errorf("round-trip mismatch: in=%+v back=%+v", in, back)
	}
}

// Pointer-to-struct embeds should also work: the populator must
// allocate the embedded struct on demand so the leaf is settable.
// The embedded type must be exported because Go disallows
// reflect.Set on unexported fields, including the synthesised
// field that anonymous embeds produce.
type EmbeddedSkipFilter struct {
	Skip string `flag:"skip" desc:"comma-separated job names to skip"`
	Only string `flag:"only" desc:"comma-separated job names to run exclusively"`
}

type embedOuterPtr struct {
	Version string `flag:"version"`
	*EmbeddedSkipFilter
}

func TestPopulateInputs_AnonymousPointerEmbedAllocates(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(embedOuterPtr{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var in embedOuterPtr
	err = populateInputs(schema, reflect.ValueOf(&in).Elem(), map[string]string{
		"version": "v1",
		"skip":    "x",
	})
	if err != nil {
		t.Fatalf("populate: %v", err)
	}
	if in.EmbeddedSkipFilter == nil {
		t.Fatal("pointer embed not allocated")
	}
	if in.Skip != "x" {
		t.Errorf("Skip=%q via pointer embed", in.Skip)
	}
}

// Outer flags shadow inner flags with the same name (Go embedding
// semantics). The outer wins; the inner is silently dropped.
type embedShadowed struct {
	Version string `flag:"version" desc:"outer wins"`
	embeddedVersion
}

type embeddedVersion struct {
	Version string `flag:"version" desc:"inner is shadowed"`
	Skip    string `flag:"skip"`
}

func TestParseInputsSchema_OuterShadowsEmbeddedFlag(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(embedShadowed{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(schema.Fields) != 2 {
		t.Fatalf("want 2 fields (version outer + skip), got %d: %+v", len(schema.Fields), schema.Fields)
	}
	for _, f := range schema.Fields {
		if f.Name == "version" && f.Description != "outer wins" {
			t.Errorf("inner version shadowed outer: desc=%q", f.Description)
		}
	}
}

// Nested anonymous embeds (struct embeds struct embeds struct) are
// walked transitively.
type level3 struct {
	Deep string `flag:"deep"`
}
type level2 struct {
	level3
	Mid string `flag:"mid"`
}
type level1 struct {
	level2
	Top string `flag:"top"`
}

func TestParseInputsSchema_NestedEmbedTransitive(t *testing.T) {
	schema, err := parseInputsSchema(reflect.TypeOf(level1{}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string]bool{}
	for _, f := range schema.Fields {
		got[f.Name] = true
	}
	for _, want := range []string{"top", "mid", "deep"} {
		if !got[want] {
			t.Errorf("nested embed missing flag %q (got %v)", want, got)
		}
	}
}

// --- Pipeline registration end-to-end with embedded args ---

type embeddedRegPipe struct{ captured embedOuter }

func (p *embeddedRegPipe) Plan(_ context.Context, plan *Plan, in embedOuter, rc RunContext) error {
	p.captured = in
	Job(plan, rc.Pipeline, func(ctx context.Context) error { return nil })
	return nil
}

func TestRegister_EmbeddedArgsAppearInSchema(t *testing.T) {
	captured := &embeddedRegPipe{}
	Register[embedOuter]("embedded-args-fixture", func() Pipeline[embedOuter] { return captured })
	reg, ok := Lookup("embedded-args-fixture")
	if !ok {
		t.Fatal("not registered")
	}
	if len(reg.Schema.Fields) != 3 {
		t.Fatalf("want 3 schema fields (version, skip, only), got %d: %+v", len(reg.Schema.Fields), reg.Schema.Fields)
	}
}
