package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

func (t *Tenant) mutateCredentials(ctx context.Context, a capability.Authorized, write func(*storeTx, string) error) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		return write(tx, p.Capability.Subject.ID)
	})
}

// WriteSecret stores a secret under current tenant administration authority.
func (t *Tenant) WriteSecret(ctx context.Context, a capability.Authorized, sealed SealedSecret) error {
	if sealed.team != t.team || sealed.team == "" || !secrets.IsBound(sealed.row.Value) {
		return ErrInvalidInput
	}
	row := sealed.row
	return t.mutateCredentials(ctx, a, func(tx *storeTx, actor string) error {
		if err := secretname.Validate(row.Name); err != nil {
			return ErrInvalidInput
		}
		if row.Principal != "" && row.Principal != actor {
			return bindingRefusal()
		}
		if holdsFreeAllowance(t.team) {
			if len(row.Value) > MaxSecretValueBytes {
				return ErrSecretLimit
			}
			var count, replacing int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN name=? AND pipeline=? THEN 1 ELSE 0 END),0) FROM secrets WHERE team=?`, row.Name, row.Pipeline, t.team).Scan(&count, &replacing); err != nil {
				return err
			}
			if replacing == 0 && count >= MaxSecretsPerTeam {
				return ErrSecretLimit
			}
		}
		at := tx.now().UnixNano()
		_, err := tx.ExecContext(ctx, `INSERT INTO secrets(team,name,value,principal,pipeline,masked,shared,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(team,name,pipeline) DO UPDATE SET value=excluded.value,principal=excluded.principal,masked=excluded.masked,shared=excluded.shared,updated_at=excluded.updated_at`, t.team, row.Name, row.Value, actor, row.Pipeline, boolInt(row.Masked), boolInt(row.Shared), at, at)
		return err
	})
}

// RemoveSecret deletes an exact secret scope under current administration authority.
func (t *Tenant) RemoveSecret(ctx context.Context, a capability.Authorized, name, pipeline string) error {
	return t.mutateCredentials(ctx, a, func(tx *storeTx, _ string) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE team=? AND name=? AND pipeline=?`, t.team, name, pipeline)
		return credentialRowChanged(result, err)
	})
}

func credentialRowChanged(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// WriteGitCredential replaces a host credential under current administration authority.
func (t *Tenant) WriteGitCredential(ctx context.Context, a capability.Authorized, sealed SealedGitCredential) (GitCredential, error) {
	if sealed.team != t.team || sealed.team == "" || !secrets.IsBound(sealed.row.Secret) {
		return GitCredential{}, ErrInvalidInput
	}
	row := sealed.row
	var out GitCredential
	err := t.mutateCredentials(ctx, a, func(tx *storeTx, actor string) error {
		if row.Team != "" && row.Team != t.team || row.CreatedBy != "" && row.CreatedBy != actor {
			return bindingRefusal()
		}
		row.CreatedBy = actor
		var err error
		out, err = t.putGitCredentialTx(ctx, tx, row, tx.now())
		return err
	})
	if err != nil {
		return GitCredential{}, err
	}
	return out, nil
}

// ConfirmGitCredentialFor confirms only the currently pinned host fingerprint.
func (t *Tenant) ConfirmGitCredentialFor(ctx context.Context, a capability.Authorized, host, repository, fingerprint string) (GitCredential, error) {
	var out GitCredential
	err := t.mutateCredentials(ctx, a, func(tx *storeTx, _ string) error {
		var err error
		out, err = scanGitCredential(tx.QueryRowContext(ctx, `SELECT `+gitCredentialCols+` FROM git_credentials WHERE team=? AND host=? AND repository_key=?`+tx.forUpdate(), t.team, host, repository))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if out.Kind != GitCredentialSSH || out.Fingerprint == "" || out.Fingerprint != fingerprint {
			return ErrFingerprintMismatch
		}
		if out.ConfirmedAt != nil {
			return nil
		}
		at := tx.now()
		result, err := tx.ExecContext(ctx, `UPDATE git_credentials SET confirmed_at=?,updated_at=? WHERE team=? AND id=? AND fingerprint=? AND confirmed_at IS NULL`, at.UnixNano(), at.UnixNano(), t.team, out.ID, fingerprint)
		if err := credentialRowChanged(result, err); err != nil {
			return err
		}
		out.ConfirmedAt, out.UpdatedAt = &at, at
		return nil
	})
	if err != nil {
		return GitCredential{}, err
	}
	return out, nil
}

// RemoveGitCredential deletes a host credential under current administration authority.
func (t *Tenant) RemoveGitCredential(ctx context.Context, a capability.Authorized, host, repository string) error {
	return t.mutateCredentials(ctx, a, func(tx *storeTx, _ string) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM git_credentials WHERE team=? AND host=? AND repository_key=?`, t.team, host, repository)
		return credentialRowChanged(result, err)
	})
}
