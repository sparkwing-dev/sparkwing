package sparkwing

import (
	"context"
	"fmt"
)

// SecretsProvider is optionally implemented by a pipeline value to
// declare its typed secrets struct. The orchestrator reads it at run
// start, resolves every required entry against the SecretResolver
// installed on ctx, fails the run loudly if any required entry is
// missing, and otherwise installs the populated struct on ctx via
// WithPipelineSecrets. Step bodies read with
// sparkwing.PipelineSecrets[T](ctx).
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

// PipelineSecrets returns the typed Secrets struct installed on ctx,
// or nil when the pipeline doesn't implement SecretsProvider.
// Panics when the installed value isn't assignable to *T -- matches
// the posture of sparkwing.Inputs[T].
func PipelineSecrets[T any](ctx context.Context) *T {
	raw := ctx.Value(keyPipelineSecrets)
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
