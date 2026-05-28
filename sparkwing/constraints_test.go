package sparkwing

import (
	"errors"
	"strings"
	"testing"
)

// applyAll is a test helper that runs each constraint's applyTo
// against a fresh fieldMeta and returns the result + the first error.
func applyAll(cs ...Constraint) (*fieldMeta, error) {
	m := &fieldMeta{}
	for _, c := range cs {
		if err := c.applyTo(m); err != nil {
			return m, err
		}
	}
	return m, nil
}

func TestRequired_SetsRequired(t *testing.T) {
	m, err := applyAll(Required())
	if err != nil {
		t.Fatalf("Required().applyTo: %v", err)
	}
	if !m.Required {
		t.Fatal("Required should set m.Required = true")
	}
}

func TestRequiredWhen_StoresPredicate(t *testing.T) {
	pred := ArgEq("target", "prod")
	m, err := applyAll(RequiredWhen(pred))
	if err != nil {
		t.Fatalf("RequiredWhen.applyTo: %v", err)
	}
	if m.RequiredWhen == nil {
		t.Fatal("RequiredWhen should store the predicate")
	}
}

func TestRequiredWhen_RejectsNilPredicate(t *testing.T) {
	_, err := applyAll(RequiredWhen(nil))
	if err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("RequiredWhen(nil) should error mentioning nil; got %v", err)
	}
}

func TestRequiredAndRequiredWhen_AreMutuallyExclusive(t *testing.T) {
	_, err := applyAll(Required(), RequiredWhen(Always()))
	if err == nil {
		t.Fatal("Required + RequiredWhen on same field should error")
	}
}

func TestRequiredWhen_RejectsDoubleSet(t *testing.T) {
	_, err := applyAll(RequiredWhen(ArgEq("a", 1)), RequiredWhen(ArgEq("b", 2)))
	if err == nil {
		t.Fatal("two RequiredWhen calls on the same field should error")
	}
}

func TestDefault_StoresValue(t *testing.T) {
	m, err := applyAll(Default(42))
	if err != nil {
		t.Fatalf("Default.applyTo: %v", err)
	}
	if !m.HasDefault || m.Default != 42 {
		t.Fatalf("Default should set HasDefault=true and Default=42; got %v / %v", m.HasDefault, m.Default)
	}
}

func TestDefault_RejectsDoubleSet(t *testing.T) {
	_, err := applyAll(Default(1), Default(2))
	if err == nil {
		t.Fatal("two Default calls on the same field should error")
	}
}

func TestDefaultAndComputed_AreMutuallyExclusive(t *testing.T) {
	_, err := applyAll(Default(1), Computed(func(args struct{}) int { return 2 }))
	if err == nil {
		t.Fatal("Default + Computed on same field should error")
	}
	_, err = applyAll(Computed(func(args struct{}) int { return 2 }), Default(1))
	if err == nil {
		t.Fatal("Computed + Default on same field should error")
	}
}

func TestComputed_StoresFunc(t *testing.T) {
	m, err := applyAll(Computed(func(args struct{}) int { return 7 }))
	if err != nil {
		t.Fatalf("Computed.applyTo: %v", err)
	}
	if !m.HasComputed || !m.Computed.IsValid() {
		t.Fatal("Computed should set HasComputed and capture the func via reflect")
	}
}

func TestComputed_RejectsNonFunc(t *testing.T) {
	_, err := applyAll(Computed(42))
	if err == nil || !strings.Contains(err.Error(), "func") {
		t.Fatalf("Computed(non-func) should error mentioning 'func'; got %v", err)
	}
}

func TestComputed_RejectsWrongArity(t *testing.T) {
	_, err := applyAll(Computed(func() int { return 1 }))
	if err == nil {
		t.Fatal("Computed func with 0 inputs should error (must be func(T) FieldType)")
	}
	_, err = applyAll(Computed(func(a, b struct{}) int { return 1 }))
	if err == nil {
		t.Fatal("Computed func with 2 inputs should error")
	}
	_, err = applyAll(Computed(func(args struct{}) (int, error) { return 1, nil }))
	if err == nil {
		t.Fatal("Computed func with 2 outputs should error")
	}
}

func TestDependsOn_AppendsNames(t *testing.T) {
	m, err := applyAll(DependsOn("a", "b"), DependsOn("c"))
	if err != nil {
		t.Fatalf("DependsOn.applyTo: %v", err)
	}
	want := []string{"a", "b", "c"}
	if len(m.DependsOn) != len(want) {
		t.Fatalf("got %v, want %v", m.DependsOn, want)
	}
	for i, n := range want {
		if m.DependsOn[i] != n {
			t.Fatalf("DependsOn[%d] = %q, want %q", i, m.DependsOn[i], n)
		}
	}
}

func TestDependsOn_RejectsEmpty(t *testing.T) {
	_, err := applyAll(DependsOn())
	if err == nil {
		t.Fatal("DependsOn() with no names should error")
	}
}

func TestBind_StoresArgName(t *testing.T) {
	m, err := applyAll(Bind("target"))
	if err != nil {
		t.Fatalf("Bind.applyTo: %v", err)
	}
	if m.Bind != "target" {
		t.Fatalf("Bind should set m.Bind = target; got %q", m.Bind)
	}
}

func TestBind_RejectsEmptyName(t *testing.T) {
	_, err := applyAll(Bind(""))
	if err == nil {
		t.Fatal("Bind with empty name should error")
	}
}

func TestBind_RejectsDoubleSet(t *testing.T) {
	_, err := applyAll(Bind("target"), Bind("runner"))
	if err == nil {
		t.Fatal("two Bind calls on the same field should error")
	}
}

func TestOneOf_StoresValues(t *testing.T) {
	m, err := applyAll(OneOf("auto", "manual", "off"))
	if err != nil {
		t.Fatalf("OneOf.applyTo: %v", err)
	}
	if !m.HasOneOf || len(m.OneOf) != 3 {
		t.Fatalf("OneOf should set 3 values; got HasOneOf=%v len=%d", m.HasOneOf, len(m.OneOf))
	}
}

func TestOneOf_RejectsEmpty(t *testing.T) {
	_, err := applyAll(OneOf())
	if err == nil {
		t.Fatal("OneOf() with no values should error")
	}
}

func TestMinMax_StoreValues(t *testing.T) {
	m, err := applyAll(Min(1), Max(100))
	if err != nil {
		t.Fatalf("Min/Max.applyTo: %v", err)
	}
	if !m.HasMin || m.Min != 1 || !m.HasMax || m.Max != 100 {
		t.Fatalf("Min/Max should store bounds; got HasMin=%v Min=%v HasMax=%v Max=%v", m.HasMin, m.Min, m.HasMax, m.Max)
	}
}

func TestRange_StoresBothBounds(t *testing.T) {
	m, err := applyAll(Range(1, 100))
	if err != nil {
		t.Fatalf("Range.applyTo: %v", err)
	}
	if !m.HasMin || m.Min != 1 || !m.HasMax || m.Max != 100 {
		t.Fatalf("Range should set both Min and Max; got %+v", m)
	}
}

func TestPositive_IsSugarForMinOne(t *testing.T) {
	m, err := applyAll(Positive())
	if err != nil {
		t.Fatalf("Positive.applyTo: %v", err)
	}
	if !m.HasMin || m.Min != 1 {
		t.Fatalf("Positive should set Min=1; got HasMin=%v Min=%v", m.HasMin, m.Min)
	}
}

func TestMin_RejectsDoubleSet(t *testing.T) {
	_, err := applyAll(Min(1), Min(2))
	if err == nil {
		t.Fatal("two Min calls on the same field should error")
	}
}

func TestCustom_StoresFunc(t *testing.T) {
	m, err := applyAll(Custom(func(args struct{}) error { return nil }))
	if err != nil {
		t.Fatalf("Custom.applyTo: %v", err)
	}
	if !m.HasCustom || !m.Custom.IsValid() {
		t.Fatal("Custom should set HasCustom and capture the func via reflect")
	}
}

func TestCustom_RejectsNonFunc(t *testing.T) {
	_, err := applyAll(Custom("not a func"))
	if err == nil || !strings.Contains(err.Error(), "func") {
		t.Fatalf("Custom(non-func) should error mentioning 'func'; got %v", err)
	}
}

func TestCustom_RejectsWrongSignature(t *testing.T) {
	cases := []struct {
		name string
		fn   any
	}{
		{"no-input", func() error { return nil }},
		{"two-inputs", func(a, b struct{}) error { return nil }},
		{"two-outputs", func(args struct{}) (error, int) { return nil, 0 }},
		{"wrong-output-type", func(args struct{}) int { return 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := applyAll(Custom(c.fn)); err == nil {
				t.Fatalf("Custom should reject %s signature", c.name)
			}
		})
	}
}

func TestCustom_RejectsDoubleSet(t *testing.T) {
	fn := func(args struct{}) error { return errors.New("x") }
	_, err := applyAll(Custom(fn), Custom(fn))
	if err == nil {
		t.Fatal("two Custom calls on the same field should error")
	}
}

func TestChained_ComposesIntoSingleMeta(t *testing.T) {
	// Realistic chain: RequiredWhen + Default + DependsOn + Min/Max + Bind.
	m, err := applyAll(
		RequiredWhen(ArgEq("target", "prod")),
		Default(3),
		DependsOn("target"),
		Min(1),
		Max(100),
		Bind("target"),
	)
	if err != nil {
		t.Fatalf("chained constraints should compose; got error: %v", err)
	}
	if m.RequiredWhen == nil || !m.HasDefault || m.Default != 3 ||
		len(m.DependsOn) != 1 || m.DependsOn[0] != "target" ||
		!m.HasMin || m.Min != 1 || !m.HasMax || m.Max != 100 ||
		m.Bind != "target" {
		t.Fatalf("chained constraints didn't compose correctly: %+v", m)
	}
}
