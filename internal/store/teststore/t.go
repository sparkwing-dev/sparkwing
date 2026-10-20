package teststore

import "context"

// T is the part of testing.TB a fixture uses; production packages never
// import testing, and this one is no exception.
type T interface {
	Helper()
	Fatal(args ...any)
	Cleanup(func())
	Context() context.Context
}
