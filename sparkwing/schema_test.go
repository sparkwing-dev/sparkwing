package sparkwing

import (
	"errors"
	"strings"
	"testing"
)

// schemaTestArgs is the canonical args struct exercised by these
// tests. It deliberately covers the typed surface the resolution
// chain will need: numerics, strings, bools, tag overrides.
type schemaTestArgs struct {
	Replicas     int    `desc:"target replica count"`
	Image        string `desc:"OCI image ref"`
	DryRun       bool   `flag:"dry-run" desc:"skip rollout"`
	PoolSize     int    `flag:"pool-size"`
	SlackWebhook string `flag:"slack-webhook"`
	NoTag        string // no flag override, no desc -- exercises kebab-case default
}

func TestSchema_BuildSucceedsForUnconstrainedStruct(t *testing.T) {
	// Zero schema entries; every field becomes a plain optional flag.
	sb := NewSchema[schemaTestArgs]()
	s, err := sb.Build()
	if err != nil {
		t.Fatalf("Build should succeed for unconstrained schema; got %v", err)
	}
	if got, want := len(s.fields), 6; got != want {
		t.Fatalf("schema should reflect all 6 struct fields; got %d", got)
	}
	// Flag inference.
	cases := map[string]string{
		"Replicas":     "replicas",
		"Image":        "image",
		"DryRun":       "dry-run",
		"PoolSize":     "pool-size",
		"SlackWebhook": "slack-webhook",
		"NoTag":        "no-tag",
	}
	for fieldName, wantFlag := range cases {
		m := s.field(fieldName)
		if m == nil {
			t.Errorf("schema missing field %q", fieldName)
			continue
		}
		if m.Flag != wantFlag {
			t.Errorf("flag for %q: got %q, want %q", fieldName, m.Flag, wantFlag)
		}
	}
	// Desc propagation from struct tag.
	if d := s.field("Replicas").Desc; d != "target replica count" {
		t.Errorf("Desc for Replicas: got %q", d)
	}
	if d := s.field("NoTag").Desc; d != "" {
		t.Errorf("Desc for NoTag: got %q (want empty)", d)
	}
}

func TestSchema_FieldReferenceNonexistentErrors(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("DoesNotExist").Required()
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "DoesNotExist") {
		t.Fatalf("Build should error on nonexistent field; got %v", err)
	}
}

func TestSchema_DefaultTypeMismatchErrors(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Image").Default(42) // string field, int default
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "Default") {
		t.Fatalf("Build should reject Default type mismatch; got %v", err)
	}
}

func TestSchema_DefaultNumericCoercionIsAllowed(t *testing.T) {
	// Default(int) on an int field is fine; we relax numeric kind
	// matching across int/int32/int64 so calls stay terse.
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Replicas").Default(int32(3))
	if _, err := sb.Build(); err != nil {
		t.Fatalf("numeric-kind coercion across int kinds should be accepted; got %v", err)
	}
}

func TestSchema_ComputedFuncSignatureValidated(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("PoolSize").Computed(func(a schemaTestArgs) int { return a.Replicas * 2 })
	if _, err := sb.Build(); err != nil {
		t.Fatalf("valid Computed signature should pass; got %v", err)
	}

	sb2 := NewSchema[schemaTestArgs]()
	sb2.Field("PoolSize").Computed(func(a int) int { return a * 2 }) // wrong arg type
	if _, err := sb2.Build(); err == nil || !strings.Contains(err.Error(), "Computed") {
		t.Fatalf("Build should reject Computed with wrong arg type; got %v", err)
	}

	sb3 := NewSchema[schemaTestArgs]()
	sb3.Field("PoolSize").Computed(func(a schemaTestArgs) string { return "x" }) // wrong return type
	if _, err := sb3.Build(); err == nil || !strings.Contains(err.Error(), "Computed") {
		t.Fatalf("Build should reject Computed with wrong return type; got %v", err)
	}
}

func TestSchema_CustomFuncSignatureValidated(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Replicas").Custom(func(a schemaTestArgs) error { return nil })
	if _, err := sb.Build(); err != nil {
		t.Fatalf("valid Custom signature should pass; got %v", err)
	}

	sb2 := NewSchema[schemaTestArgs]()
	sb2.Field("Replicas").Custom(func(a int) error { return nil })
	if _, err := sb2.Build(); err == nil || !strings.Contains(err.Error(), "Custom") {
		t.Fatalf("Build should reject Custom with wrong arg type; got %v", err)
	}
}

func TestSchema_MinMaxRequiresNumericField(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Image").Min(1) // string field
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "numeric") {
		t.Fatalf("Build should reject Min on non-numeric field; got %v", err)
	}
}

func TestSchema_OneOfTypeChecked(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Image").OneOf("auto", "manual", "off") // string field, string values: OK
	if _, err := sb.Build(); err != nil {
		t.Fatalf("OneOf with matching types should pass; got %v", err)
	}

	sb2 := NewSchema[schemaTestArgs]()
	sb2.Field("Image").OneOf("auto", 42)
	_, err := sb2.Build()
	if err == nil || !strings.Contains(err.Error(), "OneOf") {
		t.Fatalf("OneOf with mismatched type should fail; got %v", err)
	}
}

func TestSchema_DependsOnNonexistentFieldErrors(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Replicas").DependsOn("Missing")
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "DependsOn") {
		t.Fatalf("Build should reject DependsOn to nonexistent field; got %v", err)
	}
}

func TestSchema_DependencyCycleDetected(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Replicas").DependsOn("PoolSize")
	sb.Field("PoolSize").DependsOn("Replicas")
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Build should detect dependency cycle; got %v", err)
	}
}

func TestSchema_TopoOrderRespectsDependsOn(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("PoolSize").DependsOn("Replicas")
	s, err := sb.Build()
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	pos := make(map[string]int, len(s.order))
	for i, n := range s.order {
		pos[n] = i
	}
	if pos["Replicas"] >= pos["PoolSize"] {
		t.Errorf("Replicas (pos=%d) should come before PoolSize (pos=%d) in topo order", pos["Replicas"], pos["PoolSize"])
	}
}

func TestSchema_DuplicateFlagNameErrors(t *testing.T) {
	// Two struct fields can collide if their flag tags happen to match.
	type dupArgs struct {
		A string `flag:"thing"`
		B string `flag:"thing"`
	}
	sb := NewSchema[dupArgs]()
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "--thing") {
		t.Fatalf("Build should reject duplicate flag names; got %v", err)
	}
}

func TestSchema_BindRestrictedToKnownNames(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Image").Bind("runner") // not yet a supported bind
	_, err := sb.Build()
	if err == nil || !strings.Contains(err.Error(), "Bind") {
		t.Fatalf("Build should reject unknown bind name; got %v", err)
	}

	sb2 := NewSchema[schemaTestArgs]()
	sb2.Field("Image").Bind("target")
	if _, err := sb2.Build(); err != nil {
		t.Fatalf("Bind(\"target\") should be accepted; got %v", err)
	}
}

func TestSchema_GroupValidatesFieldsAndKind(t *testing.T) {
	sb := NewSchema[schemaTestArgs]()
	sb.Group("Image", "Replicas").ExactlyOne()
	if _, err := sb.Build(); err != nil {
		t.Fatalf("group with real fields and ExactlyOne should pass; got %v", err)
	}

	sb2 := NewSchema[schemaTestArgs]()
	sb2.Group("Image", "Missing").ExactlyOne()
	_, err := sb2.Build()
	if err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Fatalf("group should fail when a member field does not exist; got %v", err)
	}

	sb3 := NewSchema[schemaTestArgs]()
	sb3.Group("Image", "Replicas") // no kind set
	_, err = sb3.Build()
	if err == nil || !strings.Contains(err.Error(), "cardinality") {
		t.Fatalf("group without a kind should fail; got %v", err)
	}
}

func TestSchema_ConstraintErrorsAccumulate(t *testing.T) {
	// Two distinct problems on the same Build should surface together,
	// not stop at the first.
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Image").Min(1)             // non-numeric -> err
	sb.Field("Replicas").Default("nope") // type mismatch -> err
	_, err := sb.Build()
	if err == nil {
		t.Fatal("expected an error")
	}
	// errors.Join wraps both; both should be reachable via errors.Unwrap walk.
	count := 0
	for _, sub := range allErrors(err) {
		if strings.Contains(sub.Error(), "Min/Max") || strings.Contains(sub.Error(), "numeric") {
			count++
		}
		if strings.Contains(sub.Error(), "Default") {
			count++
		}
	}
	if count < 2 {
		t.Errorf("expected both errors surfaced; got %v", err)
	}
}

func TestSchema_FieldCalledTwiceReturnsSameBuilder(t *testing.T) {
	// Chaining across statements should accumulate on one fieldMeta.
	sb := NewSchema[schemaTestArgs]()
	sb.Field("Replicas").Required()
	sb.Field("Replicas").Min(1) // second call -- should pile on
	s, err := sb.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	m := s.field("Replicas")
	if !m.Required {
		t.Error("Required should be set from first call")
	}
	if !m.HasMin || m.Min != 1 {
		t.Errorf("Min from second call should be present; HasMin=%v Min=%v", m.HasMin, m.Min)
	}
}

func TestKebabCase_RealisticInputs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Replicas", "replicas"},
		{"PoolSize", "pool-size"},
		{"DryRun", "dry-run"},
		{"SlackWebhook", "slack-webhook"},
		{"URLToFetch", "url-to-fetch"},
		{"NoTag", "no-tag"},
		{"X", "x"},
		{"AB", "ab"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := kebabCaseFieldName(c.in); got != c.want {
				t.Errorf("kebabCaseFieldName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// allErrors flattens an error tree produced by errors.Join into a
// slice of leaf errors. Used by TestSchema_ConstraintErrorsAccumulate.
func allErrors(err error) []error {
	type unwrapper interface{ Unwrap() []error }
	if u, ok := err.(unwrapper); ok {
		var out []error
		for _, e := range u.Unwrap() {
			out = append(out, allErrors(e)...)
		}
		return out
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return append([]error{err}, allErrors(wrapped)...)
	}
	return []error{err}
}
