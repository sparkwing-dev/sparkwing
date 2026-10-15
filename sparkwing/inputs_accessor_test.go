package sparkwing

import (
	"context"
	"strings"
	"testing"
)

type deployArgs struct {
	Service string
	Env     string
}

func TestInputs_RoundTrip(t *testing.T) {
	want := deployArgs{Service: "api", Env: "prod"}
	ctx := withTestInputs(context.Background(), want)
	got := Inputs[deployArgs](ctx)
	if got != want {
		t.Fatalf("Inputs[deployArgs] = %+v, want %+v", got, want)
	}
}

func TestInputs_PanicsWithoutInstaller(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on missing installer")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Inputs[") || !strings.Contains(msg, "no inputs installed") {
			t.Fatalf("panic should mention Inputs and missing installer, got %q", msg)
		}
	}()
	_ = Inputs[deployArgs](context.Background())
}

func TestInputs_PanicsOnTypeMismatch(t *testing.T) {
	type otherArgs struct {
		Region string
	}
	ctx := withTestInputs(context.Background(), deployArgs{Service: "x"})
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on type mismatch")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "not assignable") {
			t.Fatalf("panic should say not assignable, got %q", msg)
		}
	}()
	_ = Inputs[otherArgs](ctx)
}
