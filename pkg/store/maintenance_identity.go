package store

import (
	"context"
	"time"
)

func deleteRetentionIdentityTx(ctx context.Context, tx *storeTx, now time.Time, limit int, result *RetentionResult) error {
	spent := now.Add(-30 * 24 * time.Hour).UnixNano()
	for _, step := range []struct {
		query string
		args  []any
		count *int64
	}{
		{`DELETE FROM invitations WHERE id IN (SELECT id FROM invitations WHERE accepted_at <= ? OR withdrawn_at <= ? OR expires_at <= ? ORDER BY id LIMIT ?)`, []any{spent, spent, spent, limit}, &result.Invitations},
		{`DELETE FROM sessions WHERE hash IN (SELECT s.hash FROM sessions s WHERE s.expires_at <= ? AND NOT EXISTS (SELECT 1 FROM capabilities c WHERE c.parent_kind = 'session' AND c.parent_id = s.hash) ORDER BY s.expires_at,s.hash LIMIT ?)`, []any{now.UnixNano(), limit}, &result.Sessions},
		{`DELETE FROM flow_nonces WHERE (kind,nonce) IN (SELECT kind,nonce FROM flow_nonces WHERE expires_at <= ? ORDER BY expires_at,kind,nonce LIMIT ?)`, []any{now.UnixNano(), limit}, nil},
		{`DELETE FROM github_app_deliveries WHERE digest IN (SELECT digest FROM github_app_deliveries WHERE received_at <= ? ORDER BY received_at,digest LIMIT ?)`, []any{now.Add(-GitHubAppDeliveryRetention).UnixNano(), limit}, nil},
	} {
		removed, err := tx.ExecContext(ctx, step.query, step.args...)
		if err != nil {
			return err
		}
		if step.count != nil {
			n, err := removed.RowsAffected()
			if err != nil {
				return err
			}
			*step.count = n
		}
	}
	return nil
}
