package store

import (
	"context"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type secretScope uint8

const (
	secretNone secretScope = iota
	secretPipelineScope
	secretSharedOnly
)

type secretSelection struct {
	scope           secretScope
	repo            string
	pipeline        string
	includeUnshared bool
}

// runSecretSelection selects the pipeline secrets of the repository and
// pipeline a run speaks for, and only shared team-wide secrets when
// [runspec.Provenance.ProvenRepository] names no repository.
func runSecretSelection(r runspec.SealedRequest) (secretSelection, error) {
	p := r.Provenance
	if !p.Valid() {
		return secretSelection{}, bindingRefusal()
	}
	repo := string(p.ProvenRepository())
	if p.SharedSecretsOnly() || repo == "" {
		return secretSelection{scope: secretSharedOnly}, nil
	}
	return secretSelection{scope: secretPipelineScope, repo: repo, pipeline: p.Pipeline()}, nil
}

// safety: the one rule for which award reads a declared secret, a live approval evaluator or a trigger, plan or
// node award without an expansion; route authorization and the committing release both call it. It reports
// an approval evaluator, whose evaluation fence it has already checked.
func secretReaderTx(ctx context.Context, tx *storeTx, c capability.Capability) (bool, error) {
	if c.Binding.RunID == "" || c.Delivery == capability.DeliveryBootstrap {
		return false, bindingRefusal()
	}
	if c.Unit == capability.UnitEval {
		approval, err := approvalEvaluationReaderTx(ctx, tx, c)
		if err != nil {
			return false, err
		}
		if !approval {
			return false, bindingRefusal()
		}
		return true, nil
	}
	if c.UnitID == "" || c.Binding.Expansion != nil || !slices.Contains([]capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitNode}, c.Unit) {
		return false, bindingRefusal()
	}
	return false, nil
}

func bindingSecretTx(ctx context.Context, tx *storeTx, c capability.Capability, target capability.Target) error {
	if _, err := secretReaderTx(ctx, tx, c); err != nil {
		return err
	}
	if target.SecretName == "" || target.RunID != "" && target.RunID != c.Binding.RunID {
		return bindingRefusal()
	}
	run, err := readRunTx(ctx, tx, Team(c.Team), c.Binding.RunID)
	if err != nil {
		return err
	}
	if err := checkRunHost(c, run.Request.Provenance); err != nil {
		return err
	}
	if run.Spec == nil || !slices.Contains(run.Spec.Resolved.SecretNames, target.SecretName) {
		return &apierr.Error{Code: apierr.Forbidden, Message: "the run's pipeline does not declare this secret", Details: map[string]any{"reason": apierr.ReasonSecretUndeclared}}
	}
	selection, err := runSecretSelection(run.Request)
	if err != nil {
		return err
	}
	if slices.Contains(run.Request.Request.LocalSecretNames, target.SecretName) {
		_, err := tx.store.localSecret(Team(c.Team), run.ID, target.SecretName)
		return err
	}
	_, pool, err := selectedSecretTx(ctx, tx, Team(c.Team), target.SecretName, selection)
	if err != nil {
		return err
	}
	if pool == "" {
		return nil
	}
	if run.Request.Request.Options.Runner.Set && run.Request.Request.Options.Runner.Value.Kind == runspec.RunnerHere || !slices.Contains(c.ReservedPools, pool) {
		return bindingRefusal()
	}
	serving, err := unitPoolsTx(ctx, tx, Team(c.Team), run.ID, c.Unit, c.Binding.NodeID, []string{pool})
	if err != nil {
		return err
	}
	if len(serving) == 0 {
		return bindingRefusal()
	}
	return nil
}
