package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const bootstrapUsedKey = "bootstrap_admin"

// ErrBootstrapSeedConsumed reports that the supplied seed is the one that
// bootstrapped this store and its authority has since ended. The seed has
// done its job, so a caller starting up warns that it can be removed rather
// than failing.
var ErrBootstrapSeedConsumed = errors.New("the bootstrap admin seed was already used on this store")

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
	exists, err := tx.operator.hasDeploymentAdministratorTx(ctx, tx)
	if err != nil {
		return err
	}
	if exists {
		return authorityError(apierr.Conflict, "a deployment administrator already exists")
	}
	return nil
}

// EnsureBootstrapCapability commits the supplied first-administrator seed once.
func (o *Operator) EnsureBootstrapCapability(ctx context.Context, spec capability.ProvisionSpec) (bool, error) {
	if o.s == nil {
		return false, authorityError(apierr.PolicyMissing, "bootstrap issuer is unavailable")
	}
	if err := capability.ValidateProvisionSeed(spec); err != nil {
		return false, err
	}
	if spec.Seed == "" || spec.Kind != capability.ProvisionBootstrap || spec.Authority != nil || spec.Subject != (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) || spec.Team != "" || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.ExpiresAt != nil || spec.Metered || spec.Data != nil || len(spec.Repositories) != 0 || len(spec.ReservedPools) != 0 || spec.RegistrationID != "" || !slices.Equal(spec.Grants, []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}) {
		return false, bindingRefusal()
	}
	created := false
	err := o.s.inTx(ctx, func(tx *storeTx) error {
		created = false
		prefix, err := lockBootstrapGateTx(ctx, tx)
		if err != nil {
			return err
		}
		if prefix != "" {
			if prefix != spec.Seed[:CredentialPrefixLength] {
				return authorityError(apierr.Conflict, "bootstrap has already been consumed")
			}
			var digest string
			var revoked bool
			if err := tx.QueryRowContext(ctx, `SELECT digest, revoked_at IS NOT NULL FROM capabilities WHERE prefix = ?`, prefix).Scan(&digest, &revoked); err != nil {
				return err
			}
			if want := sha256.Sum256([]byte(spec.Seed)); digest != hex.EncodeToString(want[:]) {
				return bindingRefusal()
			}
			if revoked {
				return ErrBootstrapSeedConsumed
			}
			return nil
		}
		_, err = o.s.Issue(ctx, tx, capability.IssueRequest{Kind: capability.Delegate, Provision: &spec})
		created = err == nil
		return err
	})
	return created && err == nil, err
}

// HasDeploymentAdministrator checks current unbound deployment administration authority.
func (o *Operator) HasDeploymentAdministrator(ctx context.Context) (bool, error) {
	s := o.s
	tx, err := s.beginSnapshotReadTx(ctx)
	if err != nil {
		return false, err
	}
	defer rollbackOrLog(tx)
	exists, err := tx.operator.hasDeploymentAdministratorTx(ctx, tx)
	if err != nil {
		return false, err
	}
	return exists, tx.Commit()
}

func (op *Operator) hasDeploymentAdministratorTx(ctx context.Context, tx *storeTx) (bool, error) {
	var prefixes []string
	discover := func() lockSet {
		rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE team IS NULL AND audience='controller' AND bind_kind='none' AND subject_kind IN ('operator','service') ORDER BY prefix`)
		if err != nil {
			return lockSet{err: err}
		}
		prefixes, err = scanPrefixes(rows)
		if err != nil {
			return lockSet{err: err}
		}
		var set lockSet
		for _, prefix := range prefixes {
			next := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{{Kind: capability.AuthorityCapability, ID: prefix}})
			var api *apierr.Error
			if errors.As(next.err, &api) && api.Code == apierr.Unauthenticated {
				continue
			}
			if next.err != nil {
				return next
			}
			set.rows = append(set.rows, next.rows...)
		}
		return set
	}
	if tx.snapshotRead {
		if set := discover(); set.err != nil {
			return false, set.err
		}
	} else if err := acquireLockSet(ctx, tx, discover); err != nil {
		return false, err
	}
	for _, prefix := range prefixes {
		chain, err := authorityChainTx(ctx, tx, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix}, true)
		var p capability.Principal
		if err == nil {
			states := make([]capability.AuthorityState, len(chain))
			for i, node := range chain {
				states[i] = capability.AuthorityState{Principal: node.principal}
			}
			p, err = capability.ValidateAuthority(states, tx.now(ctx))
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

func (op *Operator) finishBootstrapSetupTx(ctx context.Context, tx *storeTx, c capability.Capability) error {
	p := capability.Principal{Capability: c, Class: capability.CredentialBearer}
	if !capability.DeploymentAuthority(p) || c.Subject == (capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}) || !slices.Contains(c.Grants, capability.OperatorTokens) {
		return nil
	}
	if err := advisoryLockTx(ctx, tx, bootstrapUsedKey); err != nil {
		return err
	}
	var setup lockSet
	if err := acquireLockSet(ctx, tx, func() lockSet {
		setup = discoverSetupAuthorityLocks(ctx, tx, lockSet{})
		return setup
	}); err != nil {
		return err
	}
	for _, row := range setup.rows {
		ref := capability.AuthorityRef{Kind: capability.AuthorityKind(row.key.keys[0]), ID: row.key.keys[1]}
		if err := tx.operator.revokeAuthorityTx(ctx, tx, ref); err != nil {
			return err
		}
	}
	return nil
}

func discoverSetupAuthorityLocks(ctx context.Context, tx *storeTx, set lockSet) lockSet {
	if set.err != nil {
		return set
	}
	refs, err := tx.operator.setupAuthorityRefsTx(ctx, tx)
	if err != nil {
		set.err = err
		return set
	}
	for _, ref := range refs {
		set = discoverAuthorityWrite(ctx, tx, set, ref)
	}
	return set
}

func (op *Operator) setupAuthorityRefsTx(ctx context.Context, tx *storeTx) ([]capability.AuthorityRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE subject_kind='operator' AND subject_id='bootstrap:admin' AND (team IS NULL OR team='')`)
	if err != nil {
		return nil, err
	}
	prefixes, err := scanPrefixes(rows)
	if err != nil {
		return nil, err
	}
	var refs []capability.AuthorityRef
	for _, prefix := range prefixes {
		refs = append(refs, capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: prefix})
	}
	seen := map[capability.AuthorityRef]bool{}
	for _, ref := range refs {
		seen[ref] = true
	}
	for i := 0; i < len(refs); i++ {
		ref := refs[i]
		rows, err := tx.QueryContext(ctx, `SELECT prefix FROM capabilities WHERE parent_kind=? AND parent_id=?`, ref.Kind, ref.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			child := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: id}
			if !seen[child] {
				seen[child] = true
				refs = append(refs, child)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
		if ref.Kind != capability.AuthorityCapability {
			continue
		}
		rows, err = tx.QueryContext(ctx, `SELECT hash FROM sessions WHERE capability_prefix=?`, ref.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			child := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: id}
			if !seen[child] {
				seen[child] = true
				refs = append(refs, child)
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return refs, nil
}
