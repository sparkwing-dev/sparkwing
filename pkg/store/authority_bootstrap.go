package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const bootstrapUsedKey = "bootstrap_capability_issued"

// EnsureBootstrapCapability commits the configured seed once through the bootstrap gate.
func (s *Store) EnsureBootstrapCapability(ctx context.Context, spec capability.ProvisionSpec) (bool, error) {
	if spec.Seed == "" {
		return false, authorityError(apierr.BadRequest, "bootstrap credential required")
	}
	created := false
	err := s.inTx(ctx, func(tx *storeTx) error {
		if err := advisoryLockTx(ctx, tx, bootstrapUsedKey); err != nil {
			return err
		}
		used, err := bootstrapUsedTx(ctx, tx)
		if err != nil || used {
			return err
		}
		ready, err := hasDeploymentAdministratorTx(ctx, tx)
		if err != nil || ready {
			return err
		}
		_, err = s.Issue(ctx, tx, capability.IssueRequest{Kind: capability.Delegate, Provision: &spec})
		created = err == nil
		return err
	})
	return created && err == nil, err
}

// HasDeploymentAdministrator reads live deployment administration with the store clock.
func (s *Store) HasDeploymentAdministrator(ctx context.Context) (bool, error) {
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	ready, err := hasDeploymentAdministratorTx(ctx, tx)
	if err != nil {
		return false, err
	}
	return ready, tx.Commit()
}

func bootstrapUsedTx(ctx context.Context, tx *storeTx) (bool, error) {
	var value []byte
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, bootstrapUsedKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func consumeBootstrapGateTx(ctx context.Context, tx *storeTx, spec capability.ProvisionSpec) error {
	if spec.Kind != capability.ProvisionBootstrap || spec.Subject != (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) || spec.ExpiresAt != nil || spec.Metered || spec.RegistrationID != "" || !slices.Equal(spec.Grants, []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}) {
		return bindingRefusal()
	}
	if err := advisoryLockTx(ctx, tx, bootstrapUsedKey); err != nil {
		return err
	}
	used, err := bootstrapUsedTx(ctx, tx)
	if err != nil {
		return err
	}
	ready, err := hasDeploymentAdministratorTx(ctx, tx)
	if err != nil {
		return err
	}
	if used || ready {
		return authorityError(apierr.Conflict, "bootstrap administration is already provisioned")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES (?,?)`, bootstrapUsedKey, []byte("true"))
	return err
}

func hasDeploymentAdministratorTx(ctx context.Context, tx *storeTx) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE (team IS NULL OR team='') AND subject_kind IN ('operator','service')`)
	if err != nil {
		return false, err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return false, err
		}
		prefixes = append(prefixes, prefix)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return false, err
	}
	for _, prefix := range prefixes {
		p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
		if err != nil {
			var failure *apierr.Error
			if errors.As(err, &failure) && failure.Code == apierr.Unauthenticated {
				continue
			}
			return false, err
		}
		c := p.Capability
		if c.Audience == capability.Controller && c.Binding.Kind == capability.BindingNone && slices.Contains(c.Grants, capability.OperatorTokens) {
			return true, nil
		}
	}
	return false, nil
}
