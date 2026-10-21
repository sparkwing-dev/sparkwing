package store

import (
	"context"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (t *Tenant) credentialAdminTx(ctx context.Context, tx *storeTx, a capability.Authorized) error {
	p, err := tx.ResolveAuthority(ctx, a.Principal.Authority)
	if err != nil {
		return err
	}
	c := p.Capability
	if a.Target.Team != string(t.team) || c.Team != string(t.team) || c.Audience != capability.Controller || c.Subject.Kind != capability.SubjectAccount || c.Binding.Kind != capability.BindingNone || c.Unit != "" || c.Purpose != "" ||
		a.Access.Credential != capability.CredentialMember || a.Access.Binding != capability.BindNone || a.Access.Liveness != capability.LiveCredential || !slices.Equal(a.Access.Grants, []capability.Grant{capability.TeamAdmin}) || !slices.Contains(c.Grants, capability.TeamAdmin) {
		return bindingRefusal()
	}
	return nil
}

// CredentialRotation describes only the committed credential-maintenance attempt.
type CredentialRotation struct {
	Rotated        int                  `json:"rotated"`
	Skipped        []SecretRotationSkip `json:"skipped"`
	GitCredentials int                  `json:"git_credentials"`
}

// SecretRotationSkip identifies an unreadable secret retained during rotation.
type SecretRotationSkip struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline,omitempty"`
}

// RotateCredentials atomically rotates credentials and retains rows when secret returns skip=true.
func (t *Tenant) RotateCredentials(ctx context.Context, a capability.Authorized, secret func(Secret) (value string, skip bool, err error), git func(GitCredential) (string, error)) (CredentialRotation, error) {
	if secret == nil || git == nil {
		return CredentialRotation{}, authorityError(apierr.BadRequest, "credential transforms are required")
	}
	var result CredentialRotation
	err := t.s.inTx(ctx, func(tx *storeTx) error {
		result = CredentialRotation{Skipped: []SecretRotationSkip{}}
		if err := acquireLockSet(ctx, tx, func() lockSet {
			set := discoverAuthorityLocks(ctx, tx, []capability.AuthorityRef{a.Principal.Authority})
			set.add(lockTeam, true, string(t.team))
			return set
		}); err != nil {
			return err
		}
		if err := t.credentialAdminTx(ctx, tx, a); err != nil {
			return err
		}
		secrets, err := t.credentialSecretsTx(ctx, tx)
		if err != nil {
			return err
		}
		credentials, err := t.credentialGitTx(ctx, tx)
		if err != nil {
			return err
		}
		for _, row := range secrets {
			value, skip, err := secret(row)
			if err != nil {
				return err
			}
			if skip {
				result.Skipped = append(result.Skipped, SecretRotationSkip{Name: row.Name, Pipeline: row.Pipeline})
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE secrets SET value=? WHERE team=? AND name=? AND pipeline=?`, value, t.team, row.Name, row.Pipeline); err != nil {
				return err
			}
			result.Rotated++
		}
		for _, row := range credentials {
			value, err := git(row)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE git_credentials SET secret=? WHERE team=? AND id=?`, value, t.team, row.ID); err != nil {
				return err
			}
		}
		result.GitCredentials = len(credentials)
		return t.credentialAdminTx(ctx, tx, a)
	})
	if err != nil {
		return CredentialRotation{}, err
	}
	return result, nil
}

func (t *Tenant) credentialSecretsTx(ctx context.Context, tx *storeTx) (_ []Secret, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,value,principal,pipeline,masked,shared,created_at,updated_at FROM secrets WHERE team=? ORDER BY name,pipeline`+tx.forUpdate(), t.team)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	return scanSecretRows(rows)
}

func (t *Tenant) credentialGitTx(ctx context.Context, tx *storeTx) (_ []GitCredential, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+gitCredentialCols+` FROM git_credentials WHERE team=? ORDER BY host`+tx.forUpdate(), t.team)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []GitCredential{}
	for rows.Next() {
		row, err := scanGitCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SourceReleaseAudits reads this tenant's committed source releases newest first.
func (t *Tenant) SourceReleaseAudits(ctx context.Context, limit int) (_ []capability.SourceReleaseAudit, err error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := t.s.query(ctx, `SELECT id,team,credential_id,host,repository_key,run_id,node_id,generation,machine_id,owner_account_id,capability_prefix,released_at FROM git_credential_releases WHERE team=? ORDER BY released_at DESC,id DESC LIMIT ?`, t.team, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []capability.SourceReleaseAudit{}
	for rows.Next() {
		var row capability.SourceReleaseAudit
		var at int64
		if err := rows.Scan(&row.ID, &row.Team, &row.CredentialID, &row.Host, &row.RepositoryKey, &row.RunID, &row.NodeID, &row.Generation, &row.MachineID, &row.OwnerAccountID, &row.CapabilityPrefix, &at); err != nil {
			return nil, err
		}
		row.ReleasedAt = time.Unix(0, at).UTC()
		out = append(out, row)
	}
	return out, rows.Err()
}
