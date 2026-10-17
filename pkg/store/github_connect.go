package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (t *Tenant) githubConnectTx(ctx context.Context, a capability.Authorized, use func(*storeTx, capability.Principal) error) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		if !p.DirectAccountSession() || !a.Principal.DirectAccountSession() || p.Capability.Subject != a.Principal.Capability.Subject || p.Capability.Team != a.Principal.Capability.Team || p.Capability.Team != string(t.team) {
			return bindingRefusal()
		}
		if err := lockAccountTx(ctx, tx, p.Capability.Subject.ID); err != nil {
			return err
		}
		return use(tx, p)
	})
}

func linkedGitHubSubjectTx(ctx context.Context, tx *storeTx, account string) (string, error) {
	var subject string
	err := tx.QueryRowContext(ctx, `SELECT subject FROM identities WHERE provider=? AND account_id=?`, ProviderGitHub, account).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return subject, err
}

// LinkedGitHubSubject reads the current owner's linked GitHub identity.
func (t *Tenant) LinkedGitHubSubject(ctx context.Context, a capability.Authorized) (string, error) {
	var subject string
	err := t.githubConnectTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		var err error
		subject, err = linkedGitHubSubjectTx(ctx, tx, p.Capability.Subject.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	return subject, nil
}

// ConsumeGitHubConnect commits a live owner's one-use connection nonce.
func (t *Tenant) ConsumeGitHubConnect(ctx context.Context, a capability.Authorized, nonce string, expires time.Time) error {
	return t.githubConnectTx(ctx, a, func(tx *storeTx, _ capability.Principal) error {
		if nonce == "" || !expires.After(tx.now()) {
			return apierr.Authorization(apierr.ReasonReauthRequired, "the connect flow expired; start connecting again")
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO flow_nonces(kind,nonce,expires_at) VALUES ('github_app_connect',?,?) ON CONFLICT(kind,nonce) DO NOTHING`, nonce, expires.UnixNano())
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return apierr.Authorization(apierr.ReasonReauthRequired, "the connect flow was already used; start connecting again")
		}
		return nil
	})
}

// BindAuthorizedGitHubInstallation binds verified provider facts under the current linked owner.
func (t *Tenant) BindAuthorizedGitHubInstallation(ctx context.Context, a capability.Authorized, in GitHubAppInstallation) (GitHubAppInstallation, error) {
	var out GitHubAppInstallation
	err := t.githubConnectTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		if in.Team != "" && in.Team != t.team || in.ConnectedBy != "" && in.ConnectedBy != p.Capability.Subject.ID {
			return bindingRefusal()
		}
		subject, err := linkedGitHubSubjectTx(ctx, tx, p.Capability.Subject.ID)
		if err != nil {
			return err
		}
		if in.GitHubUserID <= 0 || subject != strconv.FormatInt(in.GitHubUserID, 10) {
			return bindingRefusal()
		}
		in.ConnectedBy = p.Capability.Subject.ID
		out, err = t.bindGitHubInstallationTx(ctx, tx, in, tx.now())
		return err
	})
	if err != nil {
		return GitHubAppInstallation{}, err
	}
	return out, nil
}
