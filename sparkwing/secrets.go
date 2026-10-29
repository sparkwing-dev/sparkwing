package sparkwing

import (
	"context"
	"fmt"
)

// SecretsProvider is optionally implemented by a pipeline value to
// declare its typed secrets struct. Discovery reports its entries so the
// host can authorize them; [PipelineSecrets] resolves each entry through
// [Secret] when a step reads the struct, and panics on a missing required
// entry. Step bodies read with sparkwing.PipelineSecrets[T](ctx).
//
//	type ReleaseSecrets struct {
//	    DeployToken string `sw:"DEPLOY_TOKEN,required"`
//	    SlackHook   string `sw:"SLACK_HOOK,optional"`
//	}
//
//	func (Release) Secrets() any { return &ReleaseSecrets{} }
//
// Fields default to required when neither ,required nor ,optional is
// set. Optional fields whose source returns ErrSecretMissing leave
// the struct field empty without failing the run.
type SecretsProvider interface {
	Secrets() any
}

// PipelineSecrets returns the typed Secrets struct installed on ctx, resolving
// it when none is installed, or nil when the pipeline doesn't implement SecretsProvider.
// Panics when the installed value isn't assignable to *T -- matches
// the posture of sparkwing.Inputs[T].
func PipelineSecrets[T any](ctx context.Context) *T {
	raw := ctx.Value(keyPipelineSecrets)
	if raw == nil {
		raw = resolveTypedSecrets(ctx)
	}
	if raw == nil {
		return nil
	}
	v, ok := raw.(*T)
	if !ok {
		var zero T
		panic(fmt.Sprintf("sparkwing: PipelineSecrets[%T]: installed secrets is %T, not *%T", zero, raw, zero))
	}
	return v
}
