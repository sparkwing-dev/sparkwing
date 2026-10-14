// Package sparkwingtest supplies a fake protocol host without a controller or store.
package sparkwingtest

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: Secret preserves masking classification so tests cannot substitute plain variables for secrets.
type Secret struct {
	Value  string
	Masked bool
}

// safety: Options injects external results without exporting runtime plumbing from the author SDK.
type Options struct {
	Runner      sparkwing.RunnerInfo
	Run         sparkwing.RunContext
	Secrets     map[string]Secret
	Outputs     map[string]json.RawMessage
	OIDCToken   func(context.Context, string) (string, error)
	Latest      func(context.Context, string, string) (json.RawMessage, error)
	RunPipeline func(context.Context, string, map[string]string) (json.RawMessage, error)
}

// safety: Host exercises author code through the same plan and operation contract as a real host.
type Host interface {
	Context(context.Context) context.Context
	Plan(context.Context, string, map[string]string) (nodeproto.PlanDoc, error)
	RunStep(context.Context, *sparkwing.Plan, string, string) (json.RawMessage, error)
	Eval(ctx context.Context, plan *sparkwing.Plan, jobID, closureID string, input json.RawMessage) (json.RawMessage, error)
	Records() []sparkwing.LogRecord
	Masks() []string
	Close() error
}

// safety: New isolates fixtures so concurrent tests cannot share secrets, outputs or protocol state.
func New(opts Options) Host { panic("unimplemented: c3-protocol-sdk") }
