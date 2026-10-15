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

// DefaultStorageReservationTTL is how long a reservation holds its bytes
// when the writer that took it never commits or releases it.
const DefaultStorageReservationTTL = time.Hour

func execStatements(ctx context.Context, tx *storeTx, script string) error {
	for _, stmt := range splitStatements(script) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// StorageReserve asks for room in one of a team's stores.
type StorageReserve struct {
	Team Team
	Kind StorageKind
	// Bytes is the write's size. With UpTo it is the most the write may
	// use, and zero or less asks for all the room there is.
	Bytes int64
	// UpTo grants as much of Bytes as the team has room for, at least one
	// byte, for a write whose size is not known before it is read.
	UpTo bool
	// TTL defaults to [DefaultStorageReservationTTL].
	TTL time.Duration
	Now time.Time
	// SmallOutput reserves a job output. One of at most
	// [MaxUnpaidOutputBytes] is always granted, whatever room the team has,
	// and still counts toward its storage.
	SmallOutput bool
}

// StorageReservation is room a writer holds until it commits or releases.
type StorageReservation struct {
	ID        string          `json:"reservation"`
	Tier      TeamStorageTier `json:"tier"`
	Granted   int64           `json:"granted_bytes"`
	Unlimited bool            `json:"unlimited,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
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

// ReserveStorage holds the team's room in one store, subject to cache share and free-slot availability.
func (s *Store) ReserveStorage(ctx context.Context, req StorageReserve) (_ StorageReservation, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return StorageReservation{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out, err := reserveStorageTx(ctx, tx, req)
	if err != nil {
		return StorageReservation{}, err
	}
	return out, tx.Commit()
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
func reserveStorageTx(ctx context.Context, tx *storeTx, req StorageReserve) (StorageReservation, error) {
	team := NormalizeTeam(req.Team)
	if err := checkStorageTeam(team, req.Kind); err != nil {
		return StorageReservation{}, err
	}
	if req.Bytes < 0 && !req.UpTo {
		return StorageReservation{}, fmt.Errorf("%w: a reservation of %d bytes", ErrInvalidInput, req.Bytes)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultStorageReservationTTL
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	id, err := newReservationID()
	if err != nil {
		return StorageReservation{}, err
	}
	standing, err := storageStandingTx(ctx, tx, team)
	if err != nil {
		return StorageReservation{}, err
	}
	if standing.Tier == TeamTierNone {
		open, err := freeSlotOpenTx(ctx, tx)
		if err != nil {
			return StorageReservation{}, err
		}
		if open {
			standing.Tier = TeamTierFree
		}
	}
	used, reserved, err := lockTeamStorageTx(ctx, tx, team, req.Kind, now)
	if err != nil {
		return StorageReservation{}, err
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
		return StorageReservation{}, freeStoragePaused(team)
	}
	out := StorageReservation{ID: id, Tier: standing.Tier, Granted: max(req.Bytes, 0), ExpiresAt: now.Add(ttl)}
	if standing.Tier == TeamTierFree && req.Kind != StorageLogs && !small {
		share := req.Kind.share(standing.AllowanceBytes)
		room := share - used - reserved
		if req.UpTo && (req.Bytes <= 0 || req.Bytes > room) {
			out.Granted = room
		}
		if room <= 0 || out.Granted > room {
			return StorageReservation{}, &StorageQuotaError{
				Principal: string(team), Limit: req.Kind.limitName(), Unit: "bytes",
				Used: used + reserved, Allowed: share, Requested: max(req.Bytes, 1),
				Remedy: "the team has no credits, so its " + string(req.Kind) +
					" keeps at most its share of the free allowance; add credits to store more",
			}
		}
	} else if req.UpTo && req.Bytes <= 0 {
		out.Unlimited = true
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO storage_reservations (id, team, store, bytes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, string(team), string(req.Kind), out.Granted, now.UnixNano(), out.ExpiresAt.UnixNano()); err != nil {
		return StorageReservation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE team_storage SET reserved_bytes = reserved_bytes + ?, updated_at = ? WHERE team = ? AND store = ?`,
		out.Granted, now.UnixNano(), string(team), string(req.Kind)); err != nil {
		return StorageReservation{}, err
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

// StorageCommit records what a write stored.
type StorageCommit struct {
	// ID is the reservation the write took; one that already expired, was
	// never taken, or is not Team's still counts Bytes on its first commit.
	// An identical retry within the receipt window counts once.
	ID   string
	Team Team
	Kind StorageKind
	// Bytes is what the write added to the store: its size, or for an
	// overwrite the difference from what it replaced, which may be
	// negative.
	Bytes int64
	Now   time.Time
}

// CommitStorage moves a reservation into the team's stored bytes: the
// reservation is dropped and c.Bytes added. A reservation of another store
// is refused.
func (s *Store) CommitStorage(ctx context.Context, c StorageCommit) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := takeStorageSlotTx(ctx, tx, c.Team, c.Bytes, c.Now); err != nil {
		return err
	}
	if _, err := commitStorageTx(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func commitStorageTx(ctx context.Context, tx *storeTx, c StorageCommit) (replayed bool, err error) {
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

// DownloadCharge asks to serve a team's download.
type DownloadCharge struct {
	Team Team
	// Bytes is what the download delivered, or for a signed URL the
	// object's size. Zero checks that the team is not yet at its cap.
	Bytes int64
	// Record charges Bytes whatever the cap says, for bytes already sent.
	Record bool
	Now    time.Time
	// FreeCapBytes and FundedCapBytes are what a team may download in a
	// UTC day by whether it pays. Zero is no cap.
	FreeCapBytes, FundedCapBytes int64
}

// DownloadCharged is a team's download day after a charge.
type DownloadCharged struct {
	Tier     TeamStorageTier `json:"tier"`
	DayBytes int64           `json:"day_bytes"`
	// CapBytes is the cap the charge was held to; zero is none.
	CapBytes int64 `json:"cap_bytes"`
}

// ErrDownloadCap matches every [*DownloadCapError].
var ErrDownloadCap = errors.New("daily download cap reached")

// DownloadCapError refuses a download past a team's daily cap.
type DownloadCapError struct {
	Team           string
	Day            string
	UsedBytes      int64
	CapBytes       int64
	RequestedBytes int64
	RetryAfter     time.Duration
	Remedy         string
}

func (e *DownloadCapError) Error() string {
	return fmt.Sprintf("%s: team %s downloaded %d of its %d bytes on %s and this download is %d; %s",
		ErrDownloadCap, e.Team, e.UsedBytes, e.CapBytes, e.Day, e.RequestedBytes, e.Remedy)
}

// Unwrap reports [ErrDownloadCap].
func (e *DownloadCapError) Unwrap() error { return ErrDownloadCap }

// ChargeDownload charges c.Bytes to the team's UTC day, or refuses with a
// [*DownloadCapError] when the team is already at its cap. The operator's
// team has no cap. The row is locked for the charge, so a download started
// after another's charge reached the cap sees it.
func (s *Store) ChargeDownload(ctx context.Context, c DownloadCharge) (_ DownloadCharged, err error) {
	team := NormalizeTeam(c.Team)
	if team == "" || c.Bytes < 0 {
		return DownloadCharged{}, fmt.Errorf("%w: a download charge needs a team and a size", ErrInvalidInput)
	}
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	day := now.Format("2006-01-02")
	tx, err := s.beginTx(ctx)
	if err != nil {
		return DownloadCharged{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out := DownloadCharged{Tier: TeamTierFunded}
	if holdsFreeAllowance(team) {
		funded, err := teamFundedTx(ctx, tx, team)
		if err != nil {
			return DownloadCharged{}, err
		}
		out.CapBytes = c.FundedCapBytes
		if !funded {
			out.Tier, out.CapBytes = TeamTierFree, c.FreeCapBytes
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO team_download_day (team, day, bytes, updated_at) VALUES (?, ?, 0, ?) ON CONFLICT (team, day) DO NOTHING`,
		string(team), day, now.UnixNano()); err != nil {
		return DownloadCharged{}, err
	}
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT bytes FROM team_download_day WHERE team = ? AND day = ?`+tx.forUpdate(),
		string(team), day).Scan(&used); err != nil {
		return DownloadCharged{}, err
	}
	// safety: only a team already at its cap is refused, so the download that
	// crosses the cap finishes and the next one waits for midnight UTC.
	if !c.Record && out.CapBytes > 0 && used >= out.CapBytes {
		remedy := "the cap resets at midnight UTC"
		if out.Tier == TeamTierFree && c.FundedCapBytes > c.FreeCapBytes {
			remedy = fmt.Sprintf("add credits to the team to raise it to %d bytes a day, or wait for midnight UTC",
				c.FundedCapBytes)
		}
		return DownloadCharged{}, &DownloadCapError{
			Team: string(team), Day: day, UsedBytes: used, CapBytes: out.CapBytes, RequestedBytes: c.Bytes,
			RetryAfter: now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now), Remedy: remedy,
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE team_download_day SET bytes = bytes + ?, updated_at = ? WHERE team = ? AND day = ?`,
		c.Bytes, now.UnixNano(), string(team), day); err != nil {
		return DownloadCharged{}, err
	}
	out.DayBytes = used + c.Bytes
	return out, tx.Commit()
}
