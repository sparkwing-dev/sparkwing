package store

import (
	"context"
	"encoding/json"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func (t *Tenant) installSubmittedEnvironmentTx(ctx context.Context, tx *storeTx, result AdmissionResult, input IntakeRequest) error {
	request := result.Request.Request
	if request.EnvironmentSource == runspec.EnvironmentCaptured {
		if len(input.SubmittedEnv) != 0 {
			return authorityError(apierr.BadRequest, "captured environment cannot carry submitted values")
		}
		return nil
	}
	if request.EnvironmentSource != runspec.EnvironmentSubmitted && (request.EnvironmentSource != "" || len(request.Env.Names) != 0 || len(input.SubmittedEnv) != 0) {
		return authorityError(apierr.BadRequest, "environment provenance is required")
	}
	values := input.SubmittedEnv
	if input.Request.Lineage.RetryOf != "" || request.Lineage.ParentRunID != "" {
		source := input.Request.Lineage.RetryOf
		if source == "" {
			source = request.Lineage.ParentRunID
		}
		var err error
		values, err = t.readSubmittedEnvironmentTx(ctx, tx, source)
		if err != nil {
			return err
		}
	}
	if len(values) == 0 && len(request.Env.Names) == 0 {
		return nil
	}
	if err := runspec.VerifyEnvironment(request.Env, values); err != nil {
		return authorityError(apierr.SpecDrift, err.Error())
	}
	if err := procenv.CheckSubmitted(values, secrets.NewMasker()); err != nil {
		return authorityError(apierr.BadRequest, "invalid submitted environment")
	}
	assignments := make([]string, 0, len(values))
	for name, value := range values {
		assignments = append(assignments, name+"="+string(value))
	}
	allowed, err := procenv.Submit(assignments, request.Options.SubmitEnvAllow.Value, secrets.NewMasker())
	if err != nil || len(allowed) != len(values) {
		return authorityError(apierr.BadRequest, "submitted environment exceeds the sealed allow-list")
	}
	if result.Replayed {
		_, err := t.readSubmittedEnvironmentTx(ctx, tx, result.RunID)
		return err
	}
	cipher := t.s.runEnvCipher
	if cipher == nil {
		return authorityError(apierr.Unavailable, "submitted environment encryption is unavailable")
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return authorityError(apierr.BadRequest, "invalid submitted environment")
	}
	envelope, err := cipher.SealBound(secrets.PurposeRunEnvironment, string(t.team), result.RunID, "run-environment", false, true, string(encoded))
	if err != nil || !secrets.IsBound(envelope) {
		return authorityError(apierr.Unavailable, "submitted environment encryption failed")
	}
	_, err = tx.ExecContext(ctx, `UPDATE runs SET submitted_env=? WHERE team=? AND id=?`, envelope, t.team, result.RunID)
	return err
}

func (t *Tenant) readSubmittedEnvironmentTx(ctx context.Context, tx *storeTx, runID string) (map[string][]byte, error) {
	run, err := readRunTx(ctx, tx, t.team, runID)
	if err != nil {
		return nil, err
	}
	var envelope string
	if err := tx.QueryRowContext(ctx, `SELECT submitted_env FROM runs WHERE team=? AND id=?`, t.team, runID).Scan(&envelope); err != nil {
		return nil, err
	}
	if run.Request.Request.EnvironmentSource == runspec.EnvironmentCaptured {
		if envelope != "" {
			return nil, authorityError(apierr.Unavailable, "captured environment has unexpected submitted values")
		}
		return nil, nil
	}
	if envelope == "" && len(run.Request.Request.Env.Names) == 0 {
		return nil, nil
	}
	cipher := t.s.runEnvCipher
	if cipher == nil {
		return nil, authorityError(apierr.Unavailable, "submitted environment encryption is unavailable")
	}
	plain, err := cipher.OpenBound(secrets.PurposeRunEnvironment, string(t.team), runID, "run-environment", false, true, envelope)
	var values map[string][]byte
	if err != nil || json.Unmarshal([]byte(plain), &values) != nil || runspec.VerifyEnvironment(run.Request.Request.Env, values) != nil {
		return nil, authorityError(apierr.Unavailable, "submitted environment is unavailable or corrupt")
	}
	return values, nil
}

// ReadSubmittedEnvironment releases private values only to the run's live execution award.
func (t *Tenant) ReadSubmittedEnvironment(ctx context.Context, authority capability.AuthorityRef) (values map[string][]byte, err error) {
	err = t.s.inTx(ctx, func(tx *storeTx) error {
		chain, err := authorityChainTx(ctx, tx, authority, false)
		if err != nil {
			return err
		}
		c := chain[0].principal.Capability
		if c.Team != string(t.team) {
			return bindingRefusal()
		}
		if err := checkExecutionUnitTx(ctx, tx, c); err != nil {
			return err
		}
		values, err = t.readSubmittedEnvironmentTx(ctx, tx, c.Binding.RunID)
		if err != nil {
			return err
		}
		if _, err := authorityChainTx(ctx, tx, authority, false); err != nil {
			return err
		}
		return checkExecutionUnitTx(ctx, tx, c)
	})
	if err != nil {
		return nil, err
	}
	return values, nil
}

func checkExecutionUnitTx(ctx context.Context, tx *storeTx, c capability.Capability) error {
	_, actor, err := renewalActor(c)
	if err != nil {
		return err
	}
	row, err := tx.operator.loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: c.Unit, UnitID: c.UnitID})
	if err != nil {
		return err
	}
	clockTime1, clockErr1 := tx.now(ctx)
	if clockErr1 != nil {
		return clockErr1
	}
	if row.team != c.Team || row.prefix != c.Prefix || row.holder != actor.Holder || runspec.ClaimGeneration(row.generation) != actor.Generation || !row.lease.Valid || row.lease.Int64 <= clockTime1.UnixNano() || row.expiry.Valid && row.expiry.Int64 <= clockTime1.UnixNano() {
		return bindingRefusal()
	}
	if c.Unit == capability.UnitEval {
		if err := bindingExpansionTx(ctx, tx, c, capability.Target{RunID: row.run, NodeID: row.node, SourceGeneration: c.Binding.Generation}); err != nil {
			return err
		}
		state, err := authenticationBindingTx(ctx, tx, c)
		if err != nil {
			return err
		}
		clockTime2, clockErr2 := tx.now(ctx)
		if clockErr2 != nil {
			return clockErr2
		}
		if state.RunCancelled || state.Expansion == nil || state.Expansion.Lease != *c.Binding.Expansion || state.Expansion.ControllerLeaseExpiresAt == nil || !state.Expansion.ControllerLeaseExpiresAt.After(clockTime2) {
			return bindingRefusal()
		}
	}
	return nil
}
