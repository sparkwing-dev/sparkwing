package store

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

type localSecretKey struct {
	team Team
	run  string
}
type localRunSecrets struct{ values map[string]string }

func validateLocalSecrets(names []string, values map[string]string) error {
	if len(names) != len(values) {
		return authorityError(apierr.BadRequest, "local secret names and resolved values must match")
	}
	seen := map[string]bool{}
	for _, name := range names {
		if secretname.Validate(name) != nil || seen[name] {
			return authorityError(apierr.BadRequest, "invalid or repeated local secret name")
		}
		if _, ok := values[name]; !ok {
			return authorityError(apierr.BadRequest, "local secret names and resolved values must match")
		}
		seen[name] = true
	}
	return nil
}

func (s *Store) installLocalSecrets(ctx context.Context, tx *storeTx, team Team, admitted AdmissionResult, values map[string]string) (*localRunSecrets, error) {
	names := admitted.Request.Request.LocalSecretNames
	if len(names) == 0 {
		return nil, nil
	}
	if admitted.Request.RunID != admitted.RunID || len(values) == 0 {
		return nil, authorityError(hosterr.Unavailable, "requester local secrets must be resolved again; submit a new run")
	}
	if err := validateLocalSecrets(names, values); err != nil {
		return nil, err
	}
	if s.localSecretsCipher != nil {
		return nil, installDurableLocalSecretsTx(ctx, tx, team, admitted, values)
	}
	key := localSecretKey{team, admitted.RunID}
	if admitted.Replayed {
		previous, present := s.localSecrets.Load(key)
		if !present {
			return nil, authorityError(hosterr.Unavailable, "requester local secrets are unavailable; submit a new run")
		}
		if !maps.Equal(previous.(*localRunSecrets).values, values) {
			return nil, authorityError(hosterr.SpecDrift, "local secret values differ from the original intake")
		}
		return nil, nil
	}
	entry := &localRunSecrets{values: maps.Clone(values)}
	previous, loaded := s.localSecrets.LoadOrStore(key, entry)
	if !loaded {
		return entry, nil
	}
	if !maps.Equal(previous.(*localRunSecrets).values, values) {
		return nil, authorityError(hosterr.SpecDrift, "local secret values differ from the original intake")
	}
	return nil, nil
}

func (s *Store) localSecret(team Team, run, name string) (string, error) {
	entry, ok := s.localSecrets.Load(localSecretKey{team, run})
	if ok {
		if value, exists := entry.(*localRunSecrets).values[name]; exists {
			return value, nil
		}
	}
	return "", authorityError(hosterr.Unavailable, "requester local secrets are unavailable; submit a new run")
}

func localSecretDeclared(specNames, localNames []string) bool {
	for _, name := range localNames {
		if !slices.Contains(specNames, name) {
			return false
		}
	}
	return true
}

// ResupplyHereSecrets restores requester inputs only from their live requester host.
func (o *Operator) ResupplyHereSecrets(ctx context.Context, authority capability.AuthorityRef, values map[string]string) error {
	var installed *localRunSecrets
	var key localSecretKey
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		o.s.uninstallLocalSecrets(key, &installed)
		p, err := tx.ResolveAuthority(ctx, authority)
		if err != nil {
			return err
		}
		c := p.Capability
		if c.Unit != capability.UnitTrigger || c.Purpose != capability.PurposeTrigger {
			return bindingRefusal()
		}
		here, err := hereObserverTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if !here {
			return bindingRefusal()
		}
		tenant := &Tenant{s: o.s, team: Team(c.Team)}
		if err := lockOwnerUnitTx(ctx, tx, tenant.team, UnitRef{Kind: c.Unit, RunID: runspec.RunID(c.Binding.RunID)}); err != nil {
			return err
		}
		if err := lockStateRunTx(ctx, tx, tenant.team, c.Binding.RunID); err != nil {
			return err
		}
		run, err := readRunTx(ctx, tx, tenant.team, c.Binding.RunID)
		if err != nil {
			return err
		}
		if run.Request.Request.Burst == nil {
			return bindingRefusal()
		}
		if err := validateLocalSecrets(run.Request.Request.LocalSecretNames, values); err != nil {
			return err
		}
		if run.CancelRequestedAt != nil {
			return nil
		}
		if err := tenant.secretClaimFenceTx(ctx, tx, c); err != nil {
			return err
		}
		key = localSecretKey{tenant.team, run.ID}
		installed, err = o.s.installLocalSecrets(ctx, tx, tenant.team, AdmissionResult{RunID: run.ID, Request: run.Request}, values)
		return err
	})
	if err != nil {
		o.s.uninstallLocalSecrets(key, &installed)
	}
	return err
}

func (s *Store) uninstallLocalSecrets(key localSecretKey, installed **localRunSecrets) {
	if *installed != nil {
		s.localSecrets.CompareAndDelete(key, *installed)
		*installed = nil
	}
}

func localSecretsUnavailable() error {
	return authorityError(hosterr.Unavailable, "requester local secrets are unavailable; submit a new run")
}

func durableLocalSecretsTx(ctx context.Context, tx *storeTx, team Team, run string) (map[string]string, error) {
	var envelope string
	err := tx.QueryRowContext(ctx, `SELECT envelope FROM local_run_secrets WHERE team=? AND run_id=?`, team, run).Scan(&envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, localSecretsUnavailable()
	}
	if err != nil {
		return nil, err
	}
	plain, err := tx.store.localSecretsCipher.OpenBound(secrets.PurposeRunEnvironment, string(team), run, "local-secrets", false, true, envelope)
	var encoded map[string][]byte
	if err != nil || json.Unmarshal([]byte(plain), &encoded) != nil {
		return nil, localSecretsUnavailable()
	}
	values := make(map[string]string, len(encoded))
	for name, value := range encoded {
		values[name] = string(value)
	}
	return values, nil
}

func installDurableLocalSecretsTx(ctx context.Context, tx *storeTx, team Team, admitted AdmissionResult, values map[string]string) error {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM local_run_secrets WHERE team=? AND run_id=?`, team, admitted.RunID).Scan(&present); err != nil {
		return err
	}
	if admitted.Replayed || present != 0 {
		previous, err := durableLocalSecretsTx(ctx, tx, team, admitted.RunID)
		if err != nil {
			return err
		}
		if !maps.Equal(previous, values) {
			return authorityError(hosterr.SpecDrift, "local secret values differ from the original intake")
		}
		return nil
	}
	var finished sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT finished_at FROM runs WHERE team=? AND id=?`, team, admitted.RunID).Scan(&finished); err != nil {
		return err
	}
	if finished.Valid {
		return nil
	}
	raw := make(map[string][]byte, len(values))
	for name, value := range values {
		raw[name] = []byte(value)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return localSecretsUnavailable()
	}
	envelope, err := tx.store.localSecretsCipher.SealBound(secrets.PurposeRunEnvironment, string(team), admitted.RunID, "local-secrets", false, true, string(encoded))
	if err != nil || !secrets.IsBound(envelope) {
		return localSecretsUnavailable()
	}
	_, err = tx.execContext(ctx, `INSERT INTO local_run_secrets(team,run_id,envelope) VALUES (?,?,?)`, team, admitted.RunID, envelope)
	return err
}

func localSecretTx(ctx context.Context, tx *storeTx, team Team, run, name string) (string, error) {
	if tx.store.localSecretsCipher == nil {
		return tx.store.localSecret(team, run, name)
	}
	values, err := durableLocalSecretsTx(ctx, tx, team, run)
	if err != nil {
		return "", err
	}
	value, ok := values[name]
	if !ok {
		return "", localSecretsUnavailable()
	}
	return value, nil
}
