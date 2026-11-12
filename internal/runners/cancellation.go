package runners

import (
	"context"
	"errors"
)

// OnlyCancellation reports whether every error leaf is cancellation or the supplied owner cause.
// Callers must establish shutdown ownership before treating the result as a clean stop.
func OnlyCancellation(err, cause error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		for _, part := range parts {
			if !OnlyCancellation(part, cause) {
				return false
			}
		}
		return len(parts) > 0
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return OnlyCancellation(wrapped, cause)
	}
	return (cause != nil && errors.Is(err, cause)) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
