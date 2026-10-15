package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func lockBootstrapGateTx(ctx context.Context, tx *storeTx) (string, error) {
	var prefix string
	err := tx.QueryRowContext(ctx, `INSERT INTO settings(key,value) VALUES ('bootstrap_admin',?) ON CONFLICT(key) DO UPDATE SET value=settings.value RETURNING value`, []byte("")).Scan(&prefix)
	return prefix, err
}

func checkBootstrapGateTx(ctx context.Context, tx *storeTx) error {
	prefix, err := lockBootstrapGateTx(ctx, tx)
	if err != nil {
		return err
	}
	if prefix != "" {
		return authorityError(apierr.Conflict, "bootstrap has already been consumed")
	}
	exists, err := hasDeploymentAdministratorTx(ctx, tx)
	if err != nil {
		return err
	}
	if exists {
		return authorityError(apierr.Conflict, "a deployment administrator already exists")
	}
	return nil
}

// EnsureBootstrapCapability commits the supplied first-administrator seed once.
func (s *Store) EnsureBootstrapCapability(ctx context.Context, spec capability.ProvisionSpec) (bool, error) {
	if s == nil {
		return false, authorityError(apierr.PolicyMissing, "bootstrap issuer is unavailable")
	}
	if err := capability.ValidateProvisionSeed(spec); err != nil {
		return false, err
	}
	if spec.Seed == "" || spec.Kind != capability.ProvisionBootstrap || spec.Authority != nil || spec.Subject != (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) || spec.Team != "" || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.ExpiresAt != nil || spec.Metered || spec.Data != nil || len(spec.Repositories) != 0 || len(spec.ReservedPools) != 0 || spec.RegistrationID != "" || !slices.Equal(spec.Grants, []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}) {
		return false, bindingRefusal()
	}
	created := false
	err := s.inTx(ctx, func(tx *storeTx) error {
		prefix, err := lockBootstrapGateTx(ctx, tx)
		if err != nil {
			return err
		}
		if prefix != "" {
			if prefix != spec.Seed[:16] {
				return authorityError(apierr.Conflict, "bootstrap has already been consumed")
			}
			p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
			if err != nil {
				return err
			}
			if p.Capability.Digest != sha256.Sum256([]byte(spec.Seed)) {
				return bindingRefusal()
			}
			return nil
		}
		_, err = s.Issue(ctx, tx, capability.IssueRequest{Kind: capability.Delegate, Provision: &spec})
		created = err == nil
		return err
	})
	return created && err == nil, err
}

// HasDeploymentAdministrator checks current unbound deployment administration authority.
func (s *Store) HasDeploymentAdministrator(ctx context.Context) (bool, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	exists, err := hasDeploymentAdministratorTx(ctx, tx)
	if err != nil {
		return false, err
	}
	return exists, tx.Commit()
}

func hasDeploymentAdministratorTx(ctx context.Context, tx *storeTx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team IS NULL AND audience='controller' AND bind_kind='none' AND subject_kind IN ('operator','service') ORDER BY prefix`)
	if err != nil {
		return false, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return false, errors.Join(err, rows.Close())
		}
		prefixes = append(prefixes, prefix)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	for _, prefix := range prefixes {
		chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, true)
		var p capability.Principal
		if err == nil {
			states := make([]capability.AuthorityState, len(chain))
			for i, node := range chain {
				states[i] = capability.AuthorityState{Principal: node.principal, MembershipValid: true}
			}
			p, err = capability.ValidateAuthority(states, tx.now())
		}
		var api *apierr.Error
		if errors.Is(err, sql.ErrNoRows) || errors.As(err, &api) && api.Code == apierr.Unauthenticated {
			continue
		}
		if err != nil {
			return false, err
		}
		if slices.Contains(p.Capability.Grants, capability.OperatorTokens) {
			return true, nil
		}
	}
	return false, nil
}
