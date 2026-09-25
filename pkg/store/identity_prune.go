package store

import (
	"context"
	"time"
)

// SpentIdentityRetention is how long an invitation, token or GitHub runner
// credential stays in the database after it stopped admitting anyone: long
// enough that a member can still see a revoked token or a lapsed invitation
// in a list, short enough that a team cycling them keeps a bounded number.
const SpentIdentityRetention = 30 * 24 * time.Hour

// IdentityPrune counts what one [Store.PruneSpentIdentity] pass deleted.
type IdentityPrune struct {
	Invitations             int64
	Tokens                  int64
	Sessions                int64
	GitHubRunnerCredentials int64
}

// PruneSpentIdentity deletes the identity rows that no longer admit anyone:
// invitations accepted, withdrawn or expired, and tokens revoked or expired,
// more than [SpentIdentityRetention] ago; browser sessions and GitHub runner
// credentials as soon as they expire. It is idempotent, and it deletes nothing
// that still authenticates or still counts toward a daily limit.
func (s *Store) PruneSpentIdentity(ctx context.Context, now time.Time) (IdentityPrune, error) {
	var out IdentityPrune
	spent := now.Add(-SpentIdentityRetention).UTC().Unix()
	at := now.UTC().Unix()
	for _, step := range []struct {
		dst  *int64
		stmt string
		args []any
	}{
		{&out.Invitations, `DELETE FROM invitations
  WHERE (accepted_at IS NOT NULL AND accepted_at <= ?)
     OR (withdrawn_at IS NOT NULL AND withdrawn_at <= ?)
     OR expires_at <= ?`, []any{spent, spent, spent}},
		{&out.Tokens, `DELETE FROM tokens
  WHERE (revoked_at IS NOT NULL AND revoked_at <= ?)
     OR (expires_at IS NOT NULL AND expires_at <= ?)`, []any{spent, spent}},
		{&out.GitHubRunnerCredentials, `DELETE FROM github_runner_credentials WHERE expires_at <= ?`, []any{at}},
	} {
		res, err := s.exec(ctx, step.stmt, step.args...)
		if err != nil {
			return out, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return out, err
		}
		*step.dst = n
	}
	sessions, err := s.ExpireSessions(now)
	out.Sessions = sessions
	return out, err
}
