package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// LocalDashboardSession is a stored browser session for the local dashboard.
// Peer is the minting socket peer's identity as JSON; Origin is the exact origin it was redeemed at.
type LocalDashboardSession struct {
	Origin    string
	Peer      string
	ExpiresAt time.Time
}

// MintLocalDashboardCode stores a one-time sign-in code digest for one origin and prunes expired rows.
func (o *Operator) MintLocalDashboardCode(ctx context.Context, digest, origin, peer string, now, expires time.Time) error {
	if digest == "" || origin == "" || peer == "" || !expires.After(now) {
		return authorityError(apierr.BadRequest, "dashboard sign-in code requires a digest, origin, peer and future expiry")
	}
	_, err := inTxResult(ctx, o.s, func(tx *storeTx) (struct{}, error) {
		if _, err := tx.execContext(ctx, `DELETE FROM local_dashboard_sessions WHERE expires_at <= ?`, now.UnixNano()); err != nil {
			return struct{}{}, err
		}
		_, err := tx.execContext(ctx, `INSERT INTO local_dashboard_sessions(digest,kind,origin,peer,expires_at) VALUES (?,?,?,?,?)`, digest, "code", origin, peer, expires.UnixNano())
		return struct{}{}, err
	})
	return err
}

// RedeemLocalDashboardCode consumes a sign-in code and stores the session it becomes.
// The code is spent even when it is expired or presented at another origin.
func (o *Operator) RedeemLocalDashboardCode(ctx context.Context, code, session, origin string, now, expires time.Time) (string, error) {
	refused := authorityError(apierr.Unauthenticated, "this sign-in link is invalid, expired, already used or for another address")
	if code == "" || session == "" || origin == "" || !expires.After(now) {
		return "", refused
	}
	// safety: a refused code is deleted and committed, so the refusal is returned after the transaction.
	type redemption struct {
		peer    string
		refused bool
	}
	result, err := inTxResult(ctx, o.s, func(tx *storeTx) (redemption, error) {
		var stored, peer string
		var expiresAt int64
		err := tx.QueryRowContext(ctx, `SELECT origin,peer,expires_at FROM local_dashboard_sessions WHERE digest=? AND kind='code'`, code).Scan(&stored, &peer, &expiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return redemption{refused: true}, nil
		}
		if err != nil {
			return redemption{}, err
		}
		if _, err := tx.execContext(ctx, `DELETE FROM local_dashboard_sessions WHERE digest=?`, code); err != nil {
			return redemption{}, err
		}
		if stored != origin || expiresAt <= now.UnixNano() {
			return redemption{refused: true}, nil
		}
		_, err = tx.execContext(ctx, `INSERT INTO local_dashboard_sessions(digest,kind,origin,peer,expires_at) VALUES (?,?,?,?,?)`, session, "session", origin, peer, expires.UnixNano())
		return redemption{peer: peer}, err
	})
	if err != nil {
		return "", err
	}
	if result.refused {
		return "", refused
	}
	return result.peer, nil
}

// LocalDashboardSessionAt reads a live session; an expired, revoked or unknown digest is ErrNotFound.
func (o *Operator) LocalDashboardSessionAt(ctx context.Context, digest string, now time.Time) (LocalDashboardSession, error) {
	var session LocalDashboardSession
	var expires int64
	err := o.s.queryRow(ctx, `SELECT origin,peer,expires_at FROM local_dashboard_sessions WHERE digest=? AND kind='session' AND expires_at > ?`, digest, now.UnixNano()).Scan(&session.Origin, &session.Peer, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return LocalDashboardSession{}, ErrNotFound
	}
	if err != nil {
		return LocalDashboardSession{}, err
	}
	session.ExpiresAt = time.Unix(0, expires)
	return session, nil
}

// RevokeLocalDashboardSession deletes a session and reports whether it was still live.
func (o *Operator) RevokeLocalDashboardSession(ctx context.Context, digest string, now time.Time) (bool, error) {
	return inTxResult(ctx, o.s, func(tx *storeTx) (bool, error) {
		var expires int64
		err := tx.QueryRowContext(ctx, `DELETE FROM local_dashboard_sessions WHERE digest=? AND kind='session' RETURNING expires_at`, digest).Scan(&expires)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && expires > now.UnixNano(), err
	})
}
