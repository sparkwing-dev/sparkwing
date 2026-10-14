//go:build !linux

package orchestrator

// ShepherdTrigger reports false: without a child subreaper there is no way to
// inherit what a killed trigger host leaves, so the caller hosts the trigger
// itself, and node processes, which lead sessions of their own, outlive a
// host killed before it ends them.
func ShepherdTrigger([]string) (bool, error) { return false, nil }
