package sparkwing

import (
	"context"
	"fmt"
	"reflect"
)

// WithArgs is the embedded helper a job uses to declare typed args.
// T uses the pipeline Inputs tag vocabulary: a field becomes a flag
// only with a `flag:"name"` tag, and `desc`, `default`, `required`,
// `enum` and `short` behave as they do on Inputs. The framework
// resolves T from the run's flags before any step runs; a missing
// required arg or a value outside `enum` fails the run. `secret` and
// `flag:",extra"` are pipeline-only and panic at registration.
//
//	type DeployArgs struct {
//	    Replicas int    `flag:"replicas" default:"3" desc:"target replica count"`
//	    Image    string `flag:"image" required:"true" desc:"OCI image ref"`
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
// Two jobs in one plan may not declare the same flag.
type WithArgs[T any] struct {
	bound *T
}

// Args returns the resolved typed args for the current run. Panics
// when called before the framework has bound them (most commonly:
// from Plan, before Work has started, or from a goroutine spawned
// outside the framework's lifecycle).
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

// BindFromAny stores resolved args. The framework calls it before the
// job's steps run; a unit test calls it to set args without a run. A
// value that is not a T returns an error.
func (w *WithArgs[T]) BindFromAny(val any) error {
	v, ok := val.(T)
	if !ok {
		var zero T
		return fmt.Errorf("sparkwing.WithArgs[%T].BindFromAny: type mismatch (got %T)", zero, val)
	}
	w.bound = &v
	return nil
}

// ArgsType returns the reflect.Type of T, which the framework reads to
// find the tags of a job that embeds WithArgs.
func (w *WithArgs[T]) ArgsType() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}

type argsHolder interface {
	ArgsType() reflect.Type
	BindFromAny(val any) error
}

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
