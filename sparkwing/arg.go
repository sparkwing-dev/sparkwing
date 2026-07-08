package sparkwing

import (
	"context"
	"fmt"
)

// keyResolvedArgs is the context-key carrying the resolved-args map
// installed by the framework after Schema.Resolve. Internal-only;
// pipeline authors reach for the typed accessor [Arg] instead of
// the raw key.
type keyResolvedArgsType struct{}

var keyResolvedArgs = keyResolvedArgsType{}

// resolvedArgsFromContext extracts the framework-installed args map
// (keyed by flag name). Returns nil + false when no args have been
// installed -- e.g. when called before the framework's dispatch
// wires the resolved set onto the run context.
func resolvedArgsFromContext(ctx context.Context) (map[string]any, bool) {
	v := ctx.Value(keyResolvedArgs)
	if v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// Arg returns a single resolved arg by its CLI flag name. The Go
// type parameter T must match the arg's declared field type; a
// mismatch returns the zero value and an error.
//
// Typical use inside a step body, when a job needs to read another
// job's arg that wasn't part of its own typed Args struct:
//
//	target, err := sparkwing.Arg[string](ctx, "target")
//	if err != nil { return err }
//
// For reads of a job's OWN typed args struct, prefer the embedded
// [WithArgs[T]].Args(ctx) accessor -- it returns the whole struct
// type-safely without per-field calls.
func Arg[T any](ctx context.Context, name string) (T, error) {
	var zero T
	args, ok := resolvedArgsFromContext(ctx)
	if !ok {
		return zero, fmt.Errorf("sparkwing.Arg(%q): no resolved args installed on context (called outside the framework's Work lifecycle?)", name)
	}
	raw, present := args[name]
	if !present {
		return zero, fmt.Errorf("sparkwing.Arg(%q): no resolved value", name)
	}
	typed, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("sparkwing.Arg[%T](%q): type mismatch (got %T)", zero, name, raw)
	}
	return typed, nil
}

// ArgOrDefault is the convenience wrapper that returns d when the
// arg isn't present or doesn't unmarshal to T. Useful for steps
// that want to read an optional arg without surfacing an error.
func ArgOrDefault[T any](ctx context.Context, name string, d T) T {
	v, err := Arg[T](ctx, name)
	if err != nil {
		return d
	}
	return v
}
