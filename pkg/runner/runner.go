// Package runner is the entry point invoked by .sparkwing/main.go in
// user repos. It exists as a thin wrapper around the orchestrator
// entry point so the orchestrator package itself stays unbound from
// the user-facing API surface -- internals can change without forcing
// every consumer to update their main.go.
package runner

import "github.com/sparkwing-dev/sparkwing/internal/orchestrator"

// Main is the entry point for a user repo's compiled pipeline binary.
// .sparkwing/main.go calls runner.Main() after blank-importing the
// user's jobs package; runner.Main then dispatches into the
// orchestrator.
func Main() { orchestrator.Main() }
