package client

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// AccountDeletionPreview is the scope to display before confirming deletion.
type AccountDeletionPreview = controllerwire.AccountDeletionPreview

// AccountDeletionResult identifies the account removed and teams queued for deletion.
type AccountDeletionResult = controllerwire.AccountDeletionResult

// AccountDeleter uses a direct account session, never a profile's delegated token.
type AccountDeleter struct{ client *engineClient }

// NewAccountDeleter authenticates the session and obtains its CSRF proof.
func NewAccountDeleter(ctx context.Context, options EngineOptions, session string) (*AccountDeleter, error) {
	c, err := newSessionEngineClient(ctx, options, session)
	if err != nil {
		return nil, err
	}
	return &AccountDeleter{client: c}, nil
}

// Preview lists every team removed and any team that must change ownership first.
func (d *AccountDeleter) Preview(ctx context.Context) (AccountDeletionPreview, error) {
	var out AccountDeletionPreview
	err := d.client.request(ctx, "GET", "/api/v1/me/deletion", "/api/v1/me/deletion", "", nil, &out)
	return out, err
}

// Delete commits the exact account and team scope the caller confirmed.
func (d *AccountDeleter) Delete(ctx context.Context, id string, teams []string) (AccountDeletionResult, error) {
	var out AccountDeletionResult
	req := controllerwire.AccountDeletionRequest{AccountID: id, Teams: teams}
	err := d.client.request(ctx, "DELETE", "/api/v1/me", "/api/v1/me", "", req, &out)
	return out, err
}
