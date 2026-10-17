package store

import (
	"context"
	"maps"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
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

func (s *Store) installLocalSecrets(team Team, admitted AdmissionResult, values map[string]string) (*localRunSecrets, error) {
	names := admitted.Request.Request.LocalSecretNames
	if len(names) == 0 {
		return nil, nil
	}
	if admitted.Request.RunID != admitted.RunID || len(values) == 0 {
		return nil, authorityError(apierr.Unavailable, "requester local secrets must be resolved again; submit a new run")
	}
	if err := validateLocalSecrets(names, values); err != nil {
		return nil, err
	}
	key := localSecretKey{team, admitted.RunID}
	if admitted.Replayed {
		if _, present := s.localSecrets.Load(key); !present {
			return nil, authorityError(apierr.Unavailable, "requester local secrets are unavailable; submit a new run")
		}
	}
	entry := &localRunSecrets{values: maps.Clone(values)}
	previous, loaded := s.localSecrets.LoadOrStore(key, entry)
	if !loaded {
		return entry, nil
	}
	if !maps.Equal(previous.(*localRunSecrets).values, values) {
		return nil, authorityError(apierr.SpecDrift, "local secret values differ from the original intake")
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
	return "", authorityError(apierr.Unavailable, "requester local secrets are unavailable; submit a new run")
}

func localSecretDeclared(specNames, localNames []string) bool {
	for _, name := range localNames {
		if !slices.Contains(specNames, name) {
			return false
		}
	}
	return true
}

// ResupplyHereSecrets restores volatile inputs only from their live requester host.
func (s *Store) ResupplyHereSecrets(ctx context.Context, authority capability.AuthorityRef, values map[string]string) error {
	var installed *localRunSecrets
	var key localSecretKey
	err := s.inTx(ctx, func(tx *storeTx) error {
		if err := lockClaimControllerTx(ctx, tx); err != nil {
			return err
		}
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
		tenant := &Tenant{s: s, team: Team(c.Team)}
		if err := lockClaimOwnerTx(ctx, tx, tenant.team, UnitRef{Kind: c.Unit, RunID: c.Binding.RunID}); err != nil {
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
		installed, err = s.installLocalSecrets(tenant.team, AdmissionResult{RunID: run.ID, Request: run.Request}, values)
		return err
	})
	if err != nil && installed != nil {
		s.localSecrets.CompareAndDelete(key, installed)
	}
	return err
}
