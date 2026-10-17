package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func (t *Tenant) githubConnectTx(ctx context.Context, a capability.Authorized, use func(*storeTx, capability.Principal, string) error) error {
	return t.teamMutationTx(ctx, a, func(tx *storeTx, p capability.Principal) error {
		if !a.Principal.DirectAccountSession() || !p.DirectAccountSession() || p.Capability.Subject != a.Principal.Capability.Subject || p.Capability.Team != a.Principal.Capability.Team || !p.Capability.IssuedAt.Equal(a.Principal.Capability.IssuedAt) {
			return bindingRefusal()
		}
		if err := lockAccountTx(ctx, tx, p.Capability.Subject.ID); err != nil {
			return err
		}
		var subject string
		err := tx.QueryRowContext(ctx, `SELECT subject FROM identities WHERE account_id=? AND provider=?`+tx.forUpdate(), p.Capability.Subject.ID, ProviderGitHub).Scan(&subject)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return use(tx, p, subject)
	})
}

// LinkedGitHubSubject reads the current direct session account's GitHub identity.
func (t *Tenant) LinkedGitHubSubject(ctx context.Context, a capability.Authorized) (string, error) {
	var out string
	err := t.githubConnectTx(ctx, a, func(_ *storeTx, _ capability.Principal, subject string) error {
		out = subject
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// ConsumeGitHubConnect commits a live administrator's unexpired connection nonce once.
func (t *Tenant) ConsumeGitHubConnect(ctx context.Context, a capability.Authorized, nonce string, expires time.Time) error {
	return t.githubConnectTx(ctx, a, func(tx *storeTx, _ capability.Principal, _ string) error {
		used, err := consumeFlowNonceTx(ctx, tx, "github_app_connect", nonce, expires)
		if err != nil {
			return err
		}
		if !used {
			return bindingRefusal()
		}
		return nil
	})
}

// BindAuthorizedGitHubInstallation commits provider facts under the current linked identity.
func (t *Tenant) BindAuthorizedGitHubInstallation(ctx context.Context, a capability.Authorized, row GitHubAppInstallation) (GitHubAppInstallation, error) {
	var out GitHubAppInstallation
	err := t.githubConnectTx(ctx, a, func(tx *storeTx, p capability.Principal, subject string) error {
		if row.Team != "" && row.Team != t.team || row.ConnectedBy != "" && row.ConnectedBy != p.Capability.Subject.ID || row.GitHubUserID <= 0 || strconv.FormatInt(row.GitHubUserID, 10) != subject || row.AccountType != "User" && row.AccountType != "Organization" {
			return bindingRefusal()
		}
		row.ConnectedBy = p.Capability.Subject.ID
		var err error
		out, err = t.bindGitHubAppInstallationTx(ctx, tx, row, tx.now())
		return err
	})
	if err != nil {
		return GitHubAppInstallation{}, err
	}
	return out, nil
}
