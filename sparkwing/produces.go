package sparkwing

import "reflect"

// Produces is a zero-size marker embedded in a Job to declare its
// output contract at the type level. The Job adder cross-validates
// the marker against the typed step returned by Work at Plan time and
// rejects mismatches; downstream consumers read the contract via
// sw.RefTo[T](node).
//
//	type Build struct {
//	    sw.Base
//	    sw.Produces[BuildOutput]
//	    // ... fields ...
//	}
//
// The marker is mandatory for typed jobs: returning a typed result step without
// Produces[T] (or vice-versa) is a Plan-time panic.
type Produces[T any] struct{}

func (Produces[T]) producedType() reflect.Type {
	return reflect.TypeFor[T]()
}

type producer interface {
	producedType() reflect.Type
}
