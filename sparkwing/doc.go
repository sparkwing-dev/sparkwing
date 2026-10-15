// Package sparkwing declares pipelines and executes the author code requested
// by a host over protocol v2. Register a Pipeline and call Main from the
// executable's main function.
//
// A Plan contains jobs. Job creates a job from a Workable or a function.
// A Workable declares its steps with Step and selects its typed result with
// Produces. RefTo and StepGet read declared job and step outputs.
//
// Memo reuses a job result. Cache declares dependency directories. Artifacts
// publishes files, and Consumes declares which producer supplies them.
// Generate declares children from an upstream result. NeedsGenerated waits
// for that expansion and its children. Plan.Approval declares a manual gate.
//
// The SDK emits declarations. The host validates them, chooses placement,
// applies policy, persists state, and decides which work is ready. Body
// environment takes effect after planning. Credentials arrive in hello and
// stay out of child-process environment variables.
//
// Use sparkwingtest to test author code with isolated host services.
package sparkwing
