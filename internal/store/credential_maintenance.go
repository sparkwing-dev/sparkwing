package store

import (
	"context"
	"encoding/json"
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
	err := t.teamMutationTx(ctx, a, "RotateCredentials", string(t.team), func(tx *storeTx, _ capability.Principal) error {
		result = CredentialRotation{Skipped: []SecretRotationSkip{}}
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
		return nil
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

// SourceReleaseAudits reads committed releases with digests for caller-supplied identifiers.
func (t *Tenant) SourceReleaseAudits(ctx context.Context, limit int) (_ []capability.SourceReleaseAudit, err error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := t.s.query(ctx, `SELECT id,ts,team,run_id,
 CASE WHEN account<>'' AND NOT EXISTS(SELECT 1 FROM accounts WHERE accounts.id=audit_events.account) THEN 'deleted account' ELSE account END,
 attrs FROM audit_events WHERE team=? AND kind='git_credential.released' ORDER BY ts DESC,id DESC LIMIT ?`, t.team, limit)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := []capability.SourceReleaseAudit{}
	for rows.Next() {
		var row capability.SourceReleaseAudit
		var at int64
		var body string
		if err := rows.Scan(&row.ID, &at, &row.Team, &row.RunID, &row.OwnerAccountID, &body); err != nil {
			return nil, err
		}
		var attrs auditSourceRelease
		if err := json.Unmarshal([]byte(body), &attrs); err != nil {
			return nil, err
		}
		row.ReleasedAt = time.Unix(0, at).UTC()
		row.CredentialID, row.RepositoryKey, row.NodeID = attrs.CredentialID.String(), attrs.Repository.String(), attrs.NodeID.String()
		row.MachineID, row.CapabilityPrefix, row.Generation = attrs.MachineID.String(), attrs.CapabilityPrefix.String(), attrs.Generation
		out = append(out, row)
	}
	return out, rows.Err()
}
