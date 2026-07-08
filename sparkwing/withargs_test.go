package sparkwing

import (
	"context"
	"strings"
	"testing"
)

type witArgsTestArgs struct {
	Replicas int
	Image    string
}

type witArgsTestJob struct {
	Base
	WithArgs[witArgsTestArgs]
}

type witArgsNoArgsJob struct {
	Base
}

func TestWithArgs_ArgsPanicsBeforeBind(t *testing.T) {
	j := &witArgsTestJob{}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Args() before bind should panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "before") {
			t.Errorf("panic message should mention timing; got %q", msg)
		}
	}()
	_ = j.Args(context.Background())
}

func TestWithArgs_BindFromAnyRoundTrips(t *testing.T) {
	j := &witArgsTestJob{}
	want := witArgsTestArgs{Replicas: 3, Image: "foo:bar"}
	if err := j.BindFromAny(want); err != nil {
		t.Fatalf("BindFromAny: %v", err)
	}
	got := j.Args(context.Background())
	if got != want {
		t.Errorf("Args() round-trip: got %+v, want %+v", got, want)
	}
}

func TestWithArgs_BindFromAnyRejectsTypeMismatch(t *testing.T) {
	j := &witArgsTestJob{}
	err := j.BindFromAny("not the right type")
	if err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("BindFromAny should reject wrong type; got %v", err)
	}
}

func TestWithArgs_ArgsType(t *testing.T) {
	j := &witArgsTestJob{}
	got := j.ArgsType()
	if got.Name() != "witArgsTestArgs" {
		t.Errorf("ArgsType: got %s, want witArgsTestArgs", got.Name())
	}
}

func TestEmbeddedArgs_FindsHolderOnJobThatEmbedsWithArgs(t *testing.T) {
	j := &witArgsTestJob{}
	holder, argsType := embeddedArgs(j)
	if holder == nil {
		t.Fatal("embeddedArgs should find the WithArgs holder")
	}
	if argsType == nil || argsType.Name() != "witArgsTestArgs" {
		t.Errorf("embeddedArgs returned wrong type: %v", argsType)
	}

	if err := holder.BindFromAny(witArgsTestArgs{Replicas: 7}); err != nil {
		t.Fatalf("holder.BindFromAny: %v", err)
	}
	if got := j.Args(context.Background()).Replicas; got != 7 {
		t.Errorf("bind via discovered holder didn't reach original job; got %d", got)
	}
}

func TestEmbeddedArgs_ReturnsNilForJobWithoutWithArgs(t *testing.T) {
	holder, argsType := embeddedArgs(&witArgsNoArgsJob{})
	if holder != nil || argsType != nil {
		t.Errorf("expected nil/nil for job without WithArgs; got holder=%v argsType=%v", holder, argsType)
	}
}

func TestEmbeddedArgs_NilAndNonStructInputs(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"nil", nil},
		{"nil-ptr", (*witArgsTestJob)(nil)},
		{"non-ptr", witArgsTestJob{}},
		{"non-struct-ptr", new(int)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			holder, argsType := embeddedArgs(c.v)
			if holder != nil || argsType != nil {
				t.Errorf("expected nil/nil; got holder=%v argsType=%v", holder, argsType)
			}
		})
	}
}
