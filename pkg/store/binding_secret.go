package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// secretPipeline is the one owner of the pipeline whose scoped secrets a run reads: the
// pipeline its sealed provenance speaks for. A child that left its parent's revision
// reads only team-wide shared secrets, because no pipeline vouched for its code.
func secretPipeline(r runspec.SealedRequest) (string, error) {
	p := r.Provenance
	if !p.Valid() {
		return "", bindingRefusal()
	}
	if p.SharedSecretsOnly() {
		return "", nil
	}
	return p.Pipeline(), nil
}

func bindingSecretTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if c.Binding.RunID == "" || target.SecretName == "" || target.RunID != "" && target.RunID != c.Binding.RunID {
		return bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), c.Binding.RunID)
	if err != nil {
		return err
	}
	if run.Spec == nil || !slices.Contains(run.Spec.Resolved.SecretNames, target.SecretName) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "the run's pipeline does not declare this secret", Details: map[string]any{"reason": apierr.ReasonSecretUndeclared}}
	}
	pipeline, err := secretPipeline(run.Request)
	if err != nil {
		return err
	}
	if slices.Contains(run.Request.Request.LocalSecretNames, target.SecretName) {
		_, err := tx.store.localSecret(Team(c.Team), run.ID, target.SecretName)
		return err
	}
	_, pool, err := selectedSecretTx(ctx, tx, Team(c.Team), target.SecretName, pipeline, true)
	if err != nil {
		return err
	}
	if pool != "" && (run.Request.Request.Options.Runner.Set && run.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere || !slices.Contains(c.ReservedPools, pool) || !slices.Contains(run.AllowedPools, pool)) {
		return bindingRefusal()
	}
	return nil
}
