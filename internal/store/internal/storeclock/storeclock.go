// Package storeclock carries the store's test clock option to teststore. It
// is internal to internal/store, so no production package can reach it.
package storeclock

import (
	"context"
	"time"
)

// Option is set by the store package at init and returns a store.Option that
// installs clock; teststore converts the result back to store.Option.
var Option func(clock func() time.Time) any

// Now reads a fixture's transaction clock without exposing it to production callers.
var Now func(context.Context, any) (time.Time, error)
