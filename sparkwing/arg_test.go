package sparkwing

import (
	"context"
	"strings"
	"testing"
)

// withResolvedArgs is a test helper that mimics what the framework
// does at dispatch time -- it installs a resolved-args map on the
// context so Arg[T] / ArgOrDefault can read from it.
func withResolvedArgs(ctx context.Context, args map[string]any) context.Context {
	return context.WithValue(ctx, keyResolvedArgs, args)
}

func TestArg_ReadsResolvedValue(t *testing.T) {
	ctx := withResolvedArgs(context.Background(), map[string]any{
		"target":   "prod",
		"replicas": 5,
		"dry-run":  true,
	})

	s, err := Arg[string](ctx, "target")
	if err != nil || s != "prod" {
		t.Errorf("string read: got (%q, %v); want (prod, nil)", s, err)
	}
	n, err := Arg[int](ctx, "replicas")
	if err != nil || n != 5 {
		t.Errorf("int read: got (%d, %v); want (5, nil)", n, err)
	}
	b, err := Arg[bool](ctx, "dry-run")
	if err != nil || !b {
		t.Errorf("bool read: got (%v, %v); want (true, nil)", b, err)
	}
}

func TestArg_NoArgsInstalledErrors(t *testing.T) {
	_, err := Arg[string](context.Background(), "target")
	if err == nil || !strings.Contains(err.Error(), "no resolved args") {
		t.Fatalf("expected error about missing context install; got %v", err)
	}
}

func TestArg_MissingNameErrors(t *testing.T) {
	ctx := withResolvedArgs(context.Background(), map[string]any{"a": 1})
	_, err := Arg[int](ctx, "missing")
	if err == nil || !strings.Contains(err.Error(), "no resolved value") {
		t.Fatalf("expected error about missing name; got %v", err)
	}
}

func TestArg_TypeMismatchErrors(t *testing.T) {
	ctx := withResolvedArgs(context.Background(), map[string]any{"replicas": "five"})
	_, err := Arg[int](ctx, "replicas")
	if err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("expected type-mismatch error; got %v", err)
	}
}

func TestArgOrDefault_FallsBackOnMissingOrMismatch(t *testing.T) {
	// No args installed -> default.
	if got := ArgOrDefault(context.Background(), "anything", 42); got != 42 {
		t.Errorf("missing context: got %d, want 42", got)
	}

	// Args installed, name missing -> default.
	ctx := withResolvedArgs(context.Background(), map[string]any{"other": 1})
	if got := ArgOrDefault(ctx, "missing", 42); got != 42 {
		t.Errorf("missing name: got %d, want 42", got)
	}

	// Type mismatch -> default.
	ctx = withResolvedArgs(context.Background(), map[string]any{"x": "not an int"})
	if got := ArgOrDefault(ctx, "x", 42); got != 42 {
		t.Errorf("type mismatch: got %d, want 42", got)
	}

	// Present + correct type -> returned value.
	ctx = withResolvedArgs(context.Background(), map[string]any{"x": 7})
	if got := ArgOrDefault(ctx, "x", 42); got != 7 {
		t.Errorf("present: got %d, want 7", got)
	}
}
