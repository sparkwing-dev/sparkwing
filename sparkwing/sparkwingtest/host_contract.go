// Package sparkwingtest supplies an in-process host for pipeline and helper-library tests.
// Host contexts execute commands and helpers locally without starting a controller or opening a store.
package sparkwingtest

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Secret preserves masking classification so tests cannot substitute plain variables for secrets.
type Secret struct {
	Value  string
	Masked bool
}

// Options injects external results without exporting runtime plumbing from the author SDK.
type Options struct {
	Runner      sparkwing.RunnerInfo
	Run         sparkwing.RunContext
	Secrets     map[string]Secret
	Outputs     map[string]json.RawMessage
	OIDCToken   func(context.Context, string) (string, error)
	Latest      func(context.Context, string, string) (json.RawMessage, error)
	RunPipeline func(context.Context, string, map[string]string) (json.RawMessage, error)
	StartChild  func(context.Context, string, map[string]string) (string, error)
	AwaitChild  func(context.Context, string) (string, error)
}

// Host exercises author code through the same plan and operation contract as a real host.
type Host interface {
	Context(context.Context) context.Context
	Plan(context.Context, string, map[string]string) (nodeproto.PlanDoc, error)
	RunStep(context.Context, *sparkwing.Plan, string, string) (json.RawMessage, error)
	Eval(ctx context.Context, plan *sparkwing.Plan, jobID, closureID string, input json.RawMessage) (json.RawMessage, error)
	// Records returns isolated log snapshots with numeric attributes represented as json.Number.
	Records() []sparkwing.LogRecord
	Masks() []string
	// Close cancels and joins active operations and service callbacks. Call it outside those calls.
	Close() error
}

// New snapshots option values so later caller edits cannot change an existing fixture.
// Service callbacks remain callable dependencies owned by the test.
// Close joins command sessions and helper resource cleanup.
func New(opts Options) Host { return newHost(opts) }
