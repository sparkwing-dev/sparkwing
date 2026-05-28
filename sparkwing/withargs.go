package sparkwing

import (
	"context"
	"fmt"
	"reflect"
)

// WithArgs is the embedded helper a job uses to declare a typed Args
// struct. The framework reflects on the job at registration time to
// discover the embedded WithArgs[T] -> T, builds the args [Schema]
// from T (plus the job's optional Schema(*SchemaBuilder[T]) method),
// and at Work-time populates a resolved T that step bodies read via
// the [Args] accessor.
//
// Typical shape:
//
//	type DeployArgs struct {
//	    Replicas int    `desc:"target replica count"`
//	    Image    string `desc:"OCI image ref"`
//	}
//
//	type DeployJob struct {
//	    sparkwing.Base
//	    sparkwing.WithArgs[DeployArgs]
//	}
//
//	func (j *DeployJob) Work(w *sparkwing.Work) (*sparkwing.WorkStep, error) {
//	    return sparkwing.Step(w, "rollout", func(ctx context.Context) error {
//	        a := j.Args(ctx)
//	        // ...
//	    }), nil
//	}
//
// Jobs that take no args simply don't embed WithArgs (or embed
// WithArgs[NoInputs] if they want to write the call uniformly).
type WithArgs[T any] struct {
	// bound stores the resolved args once the framework populates them
	// just before Work runs. Pointer so a zero-valued WithArgs[T]
	// (the common case during job construction) doesn't carry a stale
	// zero T.
	bound *T
}

// Args returns the resolved typed args for the current run. Panics
// when called before the framework has bound them (most commonly:
// from Plan, before Work has started, or from a goroutine spawned
// outside the framework's lifecycle).
//
// The ctx parameter is currently unused but kept in the signature so
// future per-context binding (e.g., per-spawn child runs) doesn't
// force a breaking API change on every job that reads its args.
func (w *WithArgs[T]) Args(ctx context.Context) T {
	_ = ctx
	if w.bound == nil {
		panic("sparkwing.WithArgs.Args: called before the framework bound args; " +
			"this usually means Args() was invoked from Plan or a goroutine " +
			"that escaped the Work lifecycle. Read args inside Step bodies " +
			"or methods Work calls synchronously.")
	}
	return *w.bound
}

// BindFromAny is called by the framework to populate the resolved
// args struct. Job code should NOT call this directly -- Go's
// visibility model doesn't have a "package-internal-callers-only"
// modifier so the method has to be exported for cross-package use
// from internal/orchestrator. The framework asserts the value to T
// before storing; type mismatches return an error rather than
// panicking so the caller can include the resolution-chain context
// in the surface error.
func (w *WithArgs[T]) BindFromAny(val any) error {
	v, ok := val.(T)
	if !ok {
		var zero T
		return fmt.Errorf("sparkwing.WithArgs[%T].BindFromAny: type mismatch (got %T)", zero, val)
	}
	w.bound = &v
	return nil
}

// ArgsType returns the reflect.Type of T. Used by the framework's
// schema-discovery pass: given a job that embeds WithArgs[T], the
// framework calls ArgsType on the embedded value to learn T without
// having to peek at the unexported bound field.
func (w *WithArgs[T]) ArgsType() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}

// argsHolder is the structural interface a WithArgs[T] satisfies.
// internal/orchestrator uses this to discover whether a job declares
// typed args (and which T to build a Schema over) without needing a
// generic type assertion (which Go doesn't support).
type argsHolder interface {
	ArgsType() reflect.Type
	BindFromAny(val any) error
}

// embeddedArgs finds the args-holder embedded in a job's struct, if
// any, and returns (its reflect-addressable handle, T). Returns
// (zero, nil) when the job doesn't embed WithArgs.
//
// Scans only the top-level anonymous fields -- args-holders nested
// behind other anonymous structs are ignored on purpose (keeps the
// job-construction model flat and discoverable).
func embeddedArgs(jobPtr any) (argsHolder, reflect.Type) {
	v := reflect.ValueOf(jobPtr)
	if !v.IsValid() {
		return nil, nil
	}
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return nil, nil
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return nil, nil
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.Anonymous {
			continue
		}
		fv := v.Field(i)
		if !fv.CanAddr() {
			continue
		}
		if holder, ok := fv.Addr().Interface().(argsHolder); ok {
			return holder, holder.ArgsType()
		}
	}
	return nil, nil
}
