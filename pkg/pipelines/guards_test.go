package pipelines_test

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestParseGuardToken_KnownShapes(t *testing.T) {
	cases := []struct {
		raw  string
		kind pipelines.GuardKind
		arg  string
		val  string
	}{
		{"where:local", pipelines.KindProfileLocal, "", ""},
		{"where:controller", pipelines.KindProfileController, "", ""},
		{"arg:image=nginx:latest", pipelines.KindArg, "image", "nginx:latest"},
	}
	for _, tc := range cases {
		tok, err := pipelines.ParseGuardToken(tc.raw)
		if err != nil {
			t.Errorf("%q: unexpected err %v", tc.raw, err)
			continue
		}
		if tok.Kind != tc.kind || tok.Arg != tc.arg || tok.Val != tc.val {
			t.Errorf("%q: got %+v, want kind=%v arg=%q val=%q", tc.raw, tok, tc.kind, tc.arg, tc.val)
		}
	}
}

func TestParseGuardToken_Errors(t *testing.T) {
	cases := []string{
		"unknown-thing",
		"where:name=",
		"where:name=prod",
		"arg:noequals",
		"arg:=novalue",
		"arg:flag=",
	}
	for _, raw := range cases {
		if _, err := pipelines.ParseGuardToken(raw); err == nil {
			t.Errorf("%q: expected parse error", raw)
		}
	}
}

func TestGuardToken_Matches(t *testing.T) {
	ctxLocal := pipelines.GuardContext{ProfileIsLocal: true}
	ctxProd := pipelines.GuardContext{ProfileIsLocal: false, Args: map[string]string{"image": "nginx"}}

	match := func(raw string, c pipelines.GuardContext) bool {
		tok, _ := pipelines.ParseGuardToken(raw)
		return tok.Matches(c)
	}

	if !match("where:local", ctxLocal) {
		t.Error("where:local should match local ctx")
	}
	if match("where:local", ctxProd) {
		t.Error("where:local should NOT match prod ctx")
	}
	if !match("where:controller", ctxProd) {
		t.Error("where:controller should match prod ctx")
	}
	if !match("arg:image=nginx", ctxProd) {
		t.Error("arg match should fire")
	}
	if match("arg:image=other", ctxProd) {
		t.Error("arg mismatch should not fire")
	}
}

func TestGuards_EvaluateRejectFiresFirst(t *testing.T) {
	g := pipelines.Guards{
		Require: []string{"arg:image=nginx"},
		Reject:  []string{"where:local"},
	}
	err := g.Evaluate("deploy-prod", pipelines.GuardContext{ProfileIsLocal: true})
	if err == nil {
		t.Fatal("expected reject to fire")
	}
	if !strings.Contains(err.Error(), "rejected") || !strings.Contains(err.Error(), "where:local") {
		t.Errorf("error should name reject + token; got %v", err)
	}
}

func TestGuards_EvaluateRequireUnsatisfied(t *testing.T) {
	g := pipelines.Guards{Require: []string{"where:controller"}}
	err := g.Evaluate("deploy-prod", pipelines.GuardContext{ProfileIsLocal: true})
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Errorf("expected require failure; got %v", err)
	}
}

func TestGuards_EvaluateAllSatisfiedNoError(t *testing.T) {
	g := pipelines.Guards{
		Require: []string{"where:controller", "arg:image=nginx"},
		Reject:  []string{"where:local"},
	}
	err := g.Evaluate("deploy-prod", pipelines.GuardContext{
		ProfileIsLocal: false,
		Args:           map[string]string{"image": "nginx"},
	})
	if err != nil {
		t.Errorf("all guards satisfied; got %v", err)
	}
}

func TestGuards_EvaluateEmptyIsNoop(t *testing.T) {
	var g pipelines.Guards
	if err := g.Evaluate("any", pipelines.GuardContext{}); err != nil {
		t.Errorf("empty guards should be a no-op; got %v", err)
	}
}

func TestParseGuardToken_ProfileNameIsRemoved(t *testing.T) {
	_, err := pipelines.ParseGuardToken("where:name=prod")
	if err == nil || !strings.Contains(err.Error(), "where:name= is removed") {
		t.Fatalf("where:name=prod = %v, want the removal named", err)
	}
}
