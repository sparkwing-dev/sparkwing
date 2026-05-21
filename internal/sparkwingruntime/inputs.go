package sparkwingruntime

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// WithInputs installs the parsed Inputs struct on ctx. Intended for
// orchestrator implementations; pipeline authors don't construct
// dispatch ctx directly. The orchestrator passes the Plan's stored
// inputs (set by the registration's invoke wrapper) so every step
// body sees the typed value the Plan() method received.
func WithInputs(ctx context.Context, in any) context.Context {
	return context.WithValue(ctx, sparkwing.RuntimePlumbing.Keys.Inputs, in)
}
