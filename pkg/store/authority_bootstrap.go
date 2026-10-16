package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
		if err != nil {
			return err
		}
		if used {
			digest := sha256.Sum256([]byte(spec.Seed))
			var prefix string
			err := tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE digest=?`, hex.EncodeToString(digest[:])).Scan(&prefix)
			if err == nil {
				p, err := tx.ResolveAuthority(ctx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
				if err == nil && p.Capability.Subject == (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) {
					return nil
				}
			}
			return authorityError(apierr.Conflict, "bootstrap seed is already used")
		}
		ready, err := hasDeploymentAdministratorTx(ctx, tx)
		if err != nil {
			return err
		}
		if ready {
			return authorityError(apierr.Conflict, "bootstrap administration is already provisioned")
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
		if capability.DeploymentAuthority(p) && slices.Contains(c.Grants, capability.OperatorTokens) {
			return true, nil
		}
	}
	return false, nil
}

func finishBootstrapSetupTx(ctx context.Context, tx *storeTx, c capability.Capability) error {
	p := capability.Principal{Capability: c, Class: capability.CredentialBearer}
	if !capability.DeploymentAuthority(p) || c.Subject == (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) || !slices.Contains(c.Grants, capability.OperatorTokens) {
		return nil
	}
	if err := advisoryLockTx(ctx, tx, bootstrapUsedKey); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE subject_kind='operator' AND subject_id='bootstrap:admin' AND (team IS NULL OR team='')`)
	if err != nil {
		return err
	}
	var prefixes []string
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			rows.Close()
			return err
		}
		prefixes = append(prefixes, prefix)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, prefix := range prefixes {
		if err := revokeSetupAuthorityTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}); err != nil {
			return err
		}
	}
	return nil
}

func revokeSetupAuthorityTx(ctx context.Context, tx *storeTx, root capability.AuthorityRef) error {
	var prefix string
	if err := tx.QueryRowContext(ctx, `SELECT prefix FROM capabilities WHERE prefix=?`+tx.forUpdate(), root.ID).Scan(&prefix); err != nil {
		return err
	}
	refs := []capability.AuthorityRef{root}
	seen := map[capability.AuthorityRef]bool{root: true}
	for i := 0; i < len(refs); i++ {
		ref := refs[i]
		rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE parent_kind=? AND parent_id=?`, ref.Kind, ref.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			child := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: id}
			if !seen[child] {
				seen[child] = true
				refs = append(refs, child)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if ref.Kind != capability.AuthorityCapability {
			continue
		}
		rows, err = tx.QueryContext(ctx, `SELECT hash FROM sessions WHERE capability_prefix=?`, ref.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			child := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}
			if !seen[child] {
				seen[child] = true
				refs = append(refs, child)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		if err := revokeAuthorityTx(ctx, tx, ref); err != nil {
			return err
		}
	}
	return nil
}
