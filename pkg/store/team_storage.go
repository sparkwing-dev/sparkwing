package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// StorageKind names the storage inventory class.
type StorageKind string

const (
	// StorageCache is the cache's binaries, dependency archives and
	// artifacts.
	StorageCache StorageKind = "cache"
	// StorageLogs is the node log segments.
	StorageLogs StorageKind = "logs"
)

// Valid reports whether k names a store the controller counts.
func (k StorageKind) Valid() bool { return k == StorageCache || k == StorageLogs }

// FreeCacheShare is the part of allowance the cache holds a free team to,
// three quarters.
func FreeCacheShare(allowance int64) int64 { return allowance / 4 * 3 }

// FreeLogShare is the part of allowance a free team's node logs are held
// to, three sixteenths.
func FreeLogShare(allowance int64) int64 { return allowance / 16 * 3 }

func (k StorageKind) share(allowance int64) int64 {
	if k == StorageLogs {
		return FreeLogShare(allowance)
	}
	return FreeCacheShare(allowance)
}

func (k StorageKind) limitName() string { return "free_" + string(k) + "_share" }

func execStatements(ctx context.Context, tx *storeTx, script string) error {
	for _, stmt := range splitStatements(script) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

type storageReserve struct {
	Team        Team
	Kind        StorageKind
	Bytes       int64
	TTL         time.Duration
	Now         time.Time
	SmallOutput bool
}

type storageReservation struct {
	ID        string
	ExpiresAt time.Time
}

func checkStorageTeam(team Team, kind StorageKind) error {
	if team == "" {
		return fmt.Errorf("%w: a storage count needs a team", ErrInvalidInput)
	}
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown store %q", ErrInvalidInput, kind)
	}
	return nil
}

func newReservationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "sr_" + hex.EncodeToString(b[:]), nil
}

// safety: the lock order puts the free tier before team_storage rows, so a
// commit takes its slot before it locks a row. A commit whose team finds every
// slot taken is refused, so no team without a slot commits a byte.
func takeStorageSlotTx(ctx context.Context, tx *storeTx, team Team, bytes int64, now time.Time) error {
	team = NormalizeTeam(team)
	if bytes <= 0 || !holdsFreeAllowance(team) {
		return nil
	}
	standing, err := storageStandingTx(ctx, tx, team)
	if err != nil || standing.Tier != TeamTierNone {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	if err := lockFreeTierTx(ctx, tx); err != nil {
		return err
	}
	slotted, err := takeFreeSlotTx(ctx, tx, team, now)
	if err != nil || slotted {
		return err
	}
	return freeStoragePaused(team)
}

// safety: a refusal returns before the first write, so a caller that keeps
// the transaction after one commits nothing of the reservation.
func reserveStorageTx(ctx context.Context, tx *storeTx, req storageReserve) (storageReservation, error) {
	team := NormalizeTeam(req.Team)
	if err := checkStorageTeam(team, req.Kind); err != nil {
		return storageReservation{}, err
	}
	if req.Bytes < 0 {
		return storageReservation{}, fmt.Errorf("%w: a reservation of %d bytes", ErrInvalidInput, req.Bytes)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	id, err := newReservationID()
	if err != nil {
		return storageReservation{}, err
	}
	standing, err := storageStandingTx(ctx, tx, team)
	if err != nil {
		return storageReservation{}, err
	}
	if standing.Tier == TeamTierNone {
		open, err := freeSlotOpenTx(ctx, tx)
		if err != nil {
			return storageReservation{}, err
		}
		if open {
			standing.Tier = TeamTierFree
		}
	}
	used, reserved, err := lockTeamStorageTx(ctx, tx, team, req.Kind, now)
	if err != nil {
		return storageReservation{}, err
	}
	// safety: a small output never fails its node, so with every slot taken
	// or its share full it may still take a team a fixed overage past the
	// share, and its bytes count.
	small := false
	if req.SmallOutput && req.Bytes <= MaxUnpaidOutputBytes && standing.Tier != TeamTierFunded {
		share := int64(0)
		if standing.Tier == TeamTierFree {
			share = req.Kind.share(standing.AllowanceBytes)
		}
		small = used+reserved+req.Bytes <= share+MaxSmallOutputOverage
	}
	if standing.Tier == TeamTierNone && !small {
		return storageReservation{}, freeStoragePaused(team)
	}
	out := storageReservation{ID: id, ExpiresAt: now.Add(ttl)}
	if standing.Tier == TeamTierFree && req.Kind != StorageLogs && !small {
		share := req.Kind.share(standing.AllowanceBytes)
		room := share - used - reserved
		if room <= 0 || req.Bytes > room {
			return storageReservation{}, &StorageQuotaError{
				Principal: string(team), Limit: req.Kind.limitName(), Unit: "bytes",
				Used: used + reserved, Allowed: share, Requested: max(req.Bytes, 1),
				Remedy: "the team has no credits, so its " + string(req.Kind) +
					" keeps at most its share of the free allowance; add credits to store more",
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO storage_reservations (id, team, store, bytes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, string(team), string(req.Kind), req.Bytes, now.UnixNano(), out.ExpiresAt.UnixNano()); err != nil {
		return storageReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE team_storage SET reserved_bytes = reserved_bytes + ?, updated_at = ? WHERE team = ? AND store = ?`,
		req.Bytes, now.UnixNano(), string(team), string(req.Kind)); err != nil {
		return storageReservation{}, err
	}
	return out, nil
}

// safety: every change to a team_storage row locks it here first, so the lock orders them.
func lockTeamStorageTx(ctx context.Context, tx *storeTx, team Team, kind StorageKind, now time.Time) (used, reserved int64, err error) {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO team_storage (team, store, used_bytes, reserved_bytes, committed_bytes, reconciled_at, updated_at)
VALUES (?, ?, 0, 0, 0, 0, ?) ON CONFLICT (team, store) DO NOTHING`,
		string(team), string(kind), now.UnixNano()); err != nil {
		return 0, 0, err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT used_bytes, reserved_bytes FROM team_storage WHERE team = ? AND store = ?`+tx.forUpdate(),
		string(team), string(kind)).Scan(&used, &reserved); err != nil {
		return 0, 0, err
	}
	var expired, expiredCount int64
	if err := tx.QueryRowContext(ctx, `
	SELECT COALESCE(SUM(bytes), 0), COUNT(*) FROM storage_reservations WHERE team = ? AND store = ? AND expires_at <= ?`,
		string(team), string(kind), now.UnixNano()).Scan(&expired, &expiredCount); err != nil {
		return 0, 0, err
	}
	if expiredCount == 0 {
		return used, reserved, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM storage_reservations WHERE team = ? AND store = ? AND expires_at <= ?`,
		string(team), string(kind), now.UnixNano()); err != nil {
		return 0, 0, err
	}
	reserved = max(reserved-expired, 0)
	_, err = tx.ExecContext(ctx, `UPDATE team_storage SET reserved_bytes = ? WHERE team = ? AND store = ?`,
		reserved, string(team), string(kind))
	return used, reserved, err
}

type reservationRow struct {
	team  Team
	kind  StorageKind
	bytes int64
}

func reservationTx(ctx context.Context, tx *storeTx, team Team, id string) (reservationRow, bool, error) {
	var r reservationRow
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT store, bytes FROM storage_reservations WHERE id = ? AND team = ?`, id, string(team)).
		Scan(&kind, &r.bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return reservationRow{}, false, nil
	}
	r.team, r.kind = team, StorageKind(kind)
	return r, err == nil, err
}

// safety: a reservation another call already dropped gives nothing back twice.
func dropReservationTx(ctx context.Context, tx *storeTx, id string, r reservationRow) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM storage_reservations WHERE id = ? AND team = ?`, id, string(r.team))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `
UPDATE team_storage SET reserved_bytes = CASE WHEN reserved_bytes > ? THEN reserved_bytes - ? ELSE 0 END
 WHERE team = ? AND store = ?`, r.bytes, r.bytes, string(r.team), string(r.kind))
	return err
}

type storageCommit struct {
	ID    string
	Team  Team
	Kind  StorageKind
	Bytes int64
	Now   time.Time
}

func commitStorageTx(ctx context.Context, tx *storeTx, c storageCommit) (replayed bool, err error) {
	team := NormalizeTeam(c.Team)
	if err := checkStorageTeam(team, c.Kind); err != nil {
		return false, err
	}
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	var r reservationRow
	var found bool
	if c.ID != "" {
		if r, found, err = reservationTx(ctx, tx, team, c.ID); err != nil {
			return false, err
		}
		if found && r.kind != c.Kind {
			return false, fmt.Errorf("%w: reservation %s is not the %s store's", ErrInvalidInput, c.ID, c.Kind)
		}
	}
	if _, _, err := lockTeamStorageTx(ctx, tx, team, c.Kind, now); err != nil {
		return false, err
	}
	if c.ID != "" {
		insert, err := tx.ExecContext(ctx, `INSERT INTO storage_commit_receipts
            (team, store, id, bytes, committed_at) VALUES (?, ?, ?, ?, ?)
            ON CONFLICT (team, store, id) DO NOTHING`,
			string(team), string(c.Kind), c.ID, c.Bytes, now.UnixNano())
		if err != nil {
			return false, err
		}
		if n, err := insert.RowsAffected(); err != nil {
			return false, err
		} else if n == 0 {
			var prior int64
			if err := tx.QueryRowContext(ctx, `SELECT bytes FROM storage_commit_receipts
                WHERE team = ? AND store = ? AND id = ?`, string(team), string(c.Kind), c.ID).Scan(&prior); err != nil {
				return false, err
			}
			if prior != c.Bytes {
				return false, fmt.Errorf("%w: reservation %s was committed with %d bytes", ErrInvalidInput, c.ID, prior)
			}
			return true, nil
		}
	}
	if found {
		if err := dropReservationTx(ctx, tx, c.ID, r); err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE team_storage SET used_bytes = CASE WHEN used_bytes + ? > 0 THEN used_bytes + ? ELSE 0 END,
       committed_bytes = committed_bytes + ?, updated_at = ?
 WHERE team = ? AND store = ?`, c.Bytes, c.Bytes, c.Bytes, now.UnixNano(), string(team), string(c.Kind))
	return false, err
}

// ReleaseStorage gives back team's reservation id, whose write stored
// nothing. A reservation already committed, released or expired, or not
// team's, is not an error.
func (s *Store) ReleaseStorage(ctx context.Context, team Team, id string, now time.Time) (err error) {
	team = NormalizeTeam(team)
	if id == "" || team == "" {
		return nil
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	r, found, err := reservationTx(ctx, tx, team, id)
	if err != nil || !found {
		return err
	}
	if _, _, err := lockTeamStorageTx(ctx, tx, r.team, r.kind, now); err != nil {
		return err
	}
	if err := dropReservationTx(ctx, tx, id, r); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) inTx(ctx context.Context, fn func(*storeTx) error) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
