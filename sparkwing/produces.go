package sparkwing

import "reflect"

// Produces is a zero-size marker embedded in a Job to declare its
// output contract at the type level. The Job adder cross-validates
// the marker against the Job's Work().SetResult at Plan time and
// rejects mismatches; downstream consumers read the contract via
// sw.Output[T](node).
//
//	type BuildJob struct {
//	    sw.Base
//	    sw.Produces[BuildOutput]
//	    // ... fields ...
//	}
//
// The marker is mandatory for typed jobs: SetResult without
// Produces[T] (or vice-versa) is a Plan-time panic.
type Produces[T any] struct{}

func (Produces[T]) producedType() reflect.Type {
	var zero T
	return reflect.TypeOf(zero)
}

type producer interface {
	producedType() reflect.Type
}
