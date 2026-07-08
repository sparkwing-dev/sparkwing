// Package runner is the entry point invoked by .sparkwing/main.go in
// user repos. It exists as a thin wrapper around the orchestrator
// entry point so the orchestrator package itself stays unbound from
// the user-facing API surface -- internals can change without forcing
// every consumer to update their main.go.
//
// A user repo's main.go is typically two lines:
//
//	package main
//
//	import _ "myrepo/.sparkwing/jobs"
//	import "github.com/sparkwing-dev/sparkwing/pkg/runner"
//
//	func main() { runner.Main() }
//
// The blank import drives every job package's init() (which calls
// sparkwing.Register), and [Main] hands control to the orchestrator.
package runner
