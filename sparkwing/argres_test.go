package sparkwing

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type argresArgs struct {
	Replicas int    `desc:"replica count"`
	Image    string `desc:"image"`
	DryRun   bool   `flag:"dry-run"`
	PoolSize int    `flag:"pool-size"`
}

func mustBuild[T any](t *testing.T, fn func(*SchemaBuilder[T])) *Schema {
	t.Helper()
	sb := NewSchema[T]()
	fn(sb)
	s, err := sb.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return s
}

func TestResolve_FlagBeatsProfileBeatsDefault(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Replicas").Default(1)
	})

	// Default kicks in: no flag, no profile -> 1.
	a, err := ResolveAs[argresArgs](s, ResolveInputs{})
	if err != nil || a.Replicas != 1 {
		t.Fatalf("default tier: got %+v err=%v", a, err)
	}

	// Profile beats default.
	a, err = ResolveAs[argresArgs](s, ResolveInputs{ProfileDefaults: map[string]string{"replicas": "5"}})
	if err != nil || a.Replicas != 5 {
		t.Fatalf("profile tier: got %+v err=%v", a, err)
	}

	// Flag beats profile.
	a, err = ResolveAs[argresArgs](s, ResolveInputs{
		FlagValues:      map[string]string{"replicas": "9"},
		ProfileDefaults: map[string]string{"replicas": "5"},
	})
	if err != nil || a.Replicas != 9 {
		t.Fatalf("flag tier: got %+v err=%v", a, err)
	}
}

func TestResolve_ComputedReadsAlreadyResolvedArgs(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Replicas").Default(3)
		sb.Field("PoolSize").
			Computed(func(a argresArgs) int { return a.Replicas * 2 }).
			DependsOn("Replicas")
	})
	a, err := ResolveAs[argresArgs](s, ResolveInputs{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.PoolSize != 6 {
		t.Errorf("PoolSize should be Replicas*2 = 6; got %d", a.PoolSize)
	}
}

func TestResolve_RequiredErrors(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Image").Required()
	})
	_, err := ResolveAs[argresArgs](s, ResolveInputs{})
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("missing required arg should error; got %v", err)
	}
}

func TestResolve_RequiredWhenContextLocal(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Image").RequiredWhen(Local)
	})

	// Local profile + no image -> error.
	_, err := ResolveAs[argresArgs](s, ResolveInputs{ProfileIsLocal: true})
	if err == nil {
		t.Fatal("RequiredWhen(Local) should fire under a local profile")
	}

	// Remote profile + no image -> ok.
	_, err = ResolveAs[argresArgs](s, ResolveInputs{ProfileIsLocal: false})
	if err != nil {
		t.Errorf("RequiredWhen(Local) should NOT fire remotely; got %v", err)
	}
}

func TestResolve_RequiredWhenPredicateReferencesAnotherArg(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Replicas").Default(0)
		sb.Field("Image").RequiredWhen(ArgEq("replicas", 3)).DependsOn("Replicas")
	})

	// replicas=3 -> image required.
	_, err := ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "3"}})
	if err == nil {
		t.Fatal("should error when replicas=3 and image missing")
	}

	// replicas=1 -> image not required.
	_, err = ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "1"}})
	if err != nil {
		t.Errorf("should pass when replicas=1; got %v", err)
	}
}

func TestResolve_OneOfRejectsValueOutsideSet(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Image").OneOf("auto", "manual", "off")
	})

	a, err := ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"image": "auto"}})
	if err != nil || a.Image != "auto" {
		t.Fatalf("OneOf should accept value in set; got %+v err=%v", a, err)
	}

	_, err = ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"image": "rogue"}})
	if err == nil || !strings.Contains(err.Error(), "OneOf") {
		t.Fatalf("OneOf should reject value outside set; got %v", err)
	}
}

func TestResolve_MinMaxEnforced(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Replicas").Range(1, 10)
	})

	// In range.
	a, err := ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "5"}})
	if err != nil || a.Replicas != 5 {
		t.Fatalf("in-range value should pass; got %+v err=%v", a, err)
	}

	// Below Min.
	_, err = ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "0"}})
	if err == nil || !strings.Contains(err.Error(), "below Min") {
		t.Fatalf("below-Min should error; got %v", err)
	}

	// Above Max.
	_, err = ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "20"}})
	if err == nil || !strings.Contains(err.Error(), "above Max") {
		t.Fatalf("above-Max should error; got %v", err)
	}
}

func TestResolve_CustomValidator(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("Replicas").Custom(func(a argresArgs) error {
			if a.Replicas%2 != 0 {
				return errors.New("must be even")
			}
			return nil
		})
	})

	a, err := ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "4"}})
	if err != nil || a.Replicas != 4 {
		t.Fatalf("even value should pass Custom; got %+v err=%v", a, err)
	}

	_, err = ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"replicas": "3"}})
	if err == nil || !strings.Contains(err.Error(), "even") {
		t.Fatalf("odd value should fail Custom; got %v", err)
	}
}

func TestResolve_GroupExactlyOne(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Group("Image", "PoolSize").ExactlyOne()
	})

	// Both set -> fail.
	_, err := ResolveAs[argresArgs](s, ResolveInputs{
		FlagValues: map[string]string{"image": "x", "pool-size": "5"},
	})
	if err == nil {
		t.Fatal("ExactlyOne with two set should fail")
	}

	// One set -> pass.
	_, err = ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"image": "x"}})
	if err != nil {
		t.Errorf("ExactlyOne with one set should pass; got %v", err)
	}

	// None set -> fail.
	_, err = ResolveAs[argresArgs](s, ResolveInputs{})
	if err == nil {
		t.Fatal("ExactlyOne with none set should fail")
	}
}

func TestResolve_BoolFlagParsing(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		sb.Field("DryRun").Default(false)
	})

	a, _ := ResolveAs[argresArgs](s, ResolveInputs{FlagValues: map[string]string{"dry-run": "true"}})
	if !a.DryRun {
		t.Error("--dry-run=true should resolve to true")
	}
	a, _ = ResolveAs[argresArgs](s, ResolveInputs{})
	if a.DryRun {
		t.Error("default false should leave DryRun false")
	}
}

func TestResolve_ProfileDefaultArgsApplyByFlagName(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {
		// no constraints; just verify profile default-args route by flag.
	})
	a, err := ResolveAs[argresArgs](s, ResolveInputs{
		ProfileDefaults: map[string]string{"pool-size": "12", "image": "foo"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.PoolSize != 12 || a.Image != "foo" {
		t.Errorf("profile defaults didn't apply by flag name; got %+v", a)
	}
}

func TestResolveAs_RejectsTypeMismatch(t *testing.T) {
	s := mustBuild(t, func(sb *SchemaBuilder[argresArgs]) {})
	type wrongArgs struct{ X int }
	_, err := ResolveAs[wrongArgs](s, ResolveInputs{})
	if err == nil || !strings.Contains(err.Error(), "schema is for") {
		t.Fatalf("ResolveAs with wrong T should error; got %v", err)
	}
}

func TestNewSchemaFromType_SynthesizesZeroConstraintSchema(t *testing.T) {
	type smallArgs struct {
		A string `desc:"a thing"`
		B int    `flag:"bcount"`
	}
	var zero smallArgs
	s, err := NewSchemaFromType(reflect.TypeOf(zero))
	if err != nil {
		t.Fatalf("NewSchemaFromType: %v", err)
	}
	if len(s.fields) != 2 {
		t.Errorf("expected 2 fields; got %d", len(s.fields))
	}
	if s.field("B").Flag != "bcount" {
		t.Errorf("flag tag override not honored; got %q", s.field("B").Flag)
	}
	if s.field("A").Desc != "a thing" {
		t.Errorf("desc tag not honored; got %q", s.field("A").Desc)
	}
	a, err := ResolveAs[smallArgs](s, ResolveInputs{FlagValues: map[string]string{"a": "hi", "bcount": "7"}})
	if err != nil {
		t.Fatalf("Resolve synthesized schema: %v", err)
	}
	if a.A != "hi" || a.B != 7 {
		t.Errorf("synthesized schema didn't resolve correctly; got %+v", a)
	}
}
