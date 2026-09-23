package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StorageKind names the store a [TeamStorageUsage] row counts.
type StorageKind string

const (
	// StorageCache is the cache's binaries, dependency archives and
	// artifacts.
	StorageCache StorageKind = "cache"
	// StorageLogs is the logs service's volume and archive.
	StorageLogs StorageKind = "logs"
)

// Valid reports whether k names a store the controller counts.
func (k StorageKind) Valid() bool { return k == StorageCache || k == StorageLogs }

// FreeCacheShare is the part of allowance the cache holds a free team to,
// three quarters.
func FreeCacheShare(allowance int64) int64 { return allowance / 4 * 3 }

// FreeLogShare is the part of allowance the logs service holds a free team
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

const teamStorageTablesSQL = `CREATE TABLE IF NOT EXISTS team_storage (
    team            TEXT NOT NULL,
    store           TEXT NOT NULL,
    used_bytes      INTEGER NOT NULL DEFAULT 0,
    reserved_bytes  INTEGER NOT NULL DEFAULT 0,
    committed_bytes INTEGER NOT NULL DEFAULT 0,
    reconciled_at   INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (team, store)
);
CREATE TABLE IF NOT EXISTS storage_reservations (
    id         TEXT PRIMARY KEY,
    team       TEXT NOT NULL,
    store      TEXT NOT NULL,
    bytes      INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_storage_reservations_team ON storage_reservations(team, store, expires_at);
CREATE INDEX IF NOT EXISTS idx_storage_reservations_expiry ON storage_reservations(expires_at);
CREATE TABLE IF NOT EXISTS team_download_day (
    team       TEXT NOT NULL,
    day        TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (team, day)
);
CREATE TABLE IF NOT EXISTS egress_day (
    service    TEXT NOT NULL,
    period     TEXT NOT NULL,
    bytes      INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (service, period)
);`

func applyTeamStorageMigrationSQLite(ctx context.Context, tx *storeTx) error {
	return execStatements(ctx, tx, teamStorageTablesSQL)
}

func applyTeamStorageMigrationPostgres(ctx context.Context, tx *storeTx) error {
	return execStatements(ctx, tx, strings.ReplaceAll(teamStorageTablesSQL, "INTEGER", "BIGINT"))
}

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
}

// StorageReservation is room a writer holds until it commits or releases.
type StorageReservation struct {
	ID        string          `json:"reservation"`
	Tier      TeamStorageTier `json:"tier"`
	Granted   int64           `json:"granted_bytes"`
	Unlimited bool            `json:"unlimited,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// TeamStorageUsage is what one team holds in one store.
type TeamStorageUsage struct {
	UsedBytes     int64     `json:"used_bytes"`
	ReservedBytes int64     `json:"reserved_bytes"`
	ReconciledAt  time.Time `json:"reconciled_at,omitzero"`
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

// ReserveStorage holds req.Bytes of the team's room in one store, or refuses
// with a [*StorageQuotaError] past a free team's share and with
// [ErrFreeStoragePaused] for a team with neither credits nor a slot. A
// funded team is granted what it asks for and counted all the same.
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

// RenewStorage commits c and reserves next in one transaction, for a writer
// that settles one block of bytes as it takes the next. The commit lands
// whether or not next fits: a refusal of next returns its error after the
// commit is kept.
func (s *Store) RenewStorage(ctx context.Context, c StorageCommit, next StorageReserve) (_ StorageReservation, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return StorageReservation{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := commitStorageTx(ctx, tx, c); err != nil {
		return StorageReservation{}, err
	}
	out, rerr := reserveStorageTx(ctx, tx, next)
	var quota *StorageQuotaError
	if rerr != nil && !errors.As(rerr, &quota) && !errors.Is(rerr, ErrFreeStoragePaused) {
		return StorageReservation{}, rerr
	}
	if err := tx.Commit(); err != nil {
		return StorageReservation{}, err
	}
	return out, rerr
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
		return StorageReservation{}, freeStoragePaused(team)
	}
	used, reserved, err := lockTeamStorageTx(ctx, tx, team, req.Kind, now)
	if err != nil {
		return StorageReservation{}, err
	}
	out := StorageReservation{ID: id, Tier: standing.Tier, Granted: max(req.Bytes, 0), ExpiresAt: now.Add(ttl)}
	if standing.Tier == TeamTierFree {
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
	var expired int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(SUM(bytes), 0) FROM storage_reservations WHERE team = ? AND store = ? AND expires_at <= ?`,
		string(team), string(kind), now.UnixNano()).Scan(&expired); err != nil {
		return 0, 0, err
	}
	if expired == 0 {
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
	// never taken, or is not Team's still counts Bytes.
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
	if err := commitStorageTx(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func commitStorageTx(ctx context.Context, tx *storeTx, c StorageCommit) (err error) {
	team := NormalizeTeam(c.Team)
	if err := checkStorageTeam(team, c.Kind); err != nil {
		return err
	}
	now := c.Now
	if now.IsZero() {
		now = time.Now()
	}
	var r reservationRow
	var found bool
	if c.ID != "" {
		if r, found, err = reservationTx(ctx, tx, team, c.ID); err != nil {
			return err
		}
		if found && r.kind != c.Kind {
			return fmt.Errorf("%w: reservation %s is not the %s store's", ErrInvalidInput, c.ID, c.Kind)
		}
	}
	if _, _, err := lockTeamStorageTx(ctx, tx, team, c.Kind, now); err != nil {
		return err
	}
	if found {
		if err := dropReservationTx(ctx, tx, c.ID, r); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE team_storage SET used_bytes = CASE WHEN used_bytes + ? > 0 THEN used_bytes + ? ELSE 0 END,
       committed_bytes = committed_bytes + ?, updated_at = ?
 WHERE team = ? AND store = ?`, c.Bytes, c.Bytes, c.Bytes, now.UnixNano(), string(team), string(c.Kind))
	return err
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

// ReleaseExpiredStorage drops every reservation that expired by now and
// reports how many rows it unblocked. A reserve drops its own team's expired
// reservations as it goes; this catches the rows nobody reserves against.
func (s *Store) ReleaseExpiredStorage(ctx context.Context, now time.Time) (int, error) {
	keys, err := s.expiredReservationRows(ctx, now)
	if err != nil {
		return 0, err
	}
	for _, k := range keys {
		if err := s.inTx(ctx, func(tx *storeTx) error {
			_, _, err := lockTeamStorageTx(ctx, tx, k.team, k.kind, now)
			return err
		}); err != nil {
			return 0, err
		}
	}
	return len(keys), nil
}

type storageRow struct {
	team Team
	kind StorageKind
}

func (s *Store) expiredReservationRows(ctx context.Context, now time.Time) (_ []storageRow, err error) {
	rows, err := s.query(ctx, `SELECT DISTINCT team, store FROM storage_reservations WHERE expires_at <= ?`, now.UnixNano())
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	var out []storageRow
	for rows.Next() {
		var team, kind string
		if err := rows.Scan(&team, &kind); err != nil {
			return nil, err
		}
		out = append(out, storageRow{Team(team), StorageKind(kind)})
	}
	return out, rows.Err()
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

// TeamStorage reports what team holds in each store the controller counts.
func (s *Store) TeamStorage(ctx context.Context, team Team) (_ map[StorageKind]TeamStorageUsage, err error) {
	rows, err := s.query(ctx, `SELECT store, used_bytes, reserved_bytes, reconciled_at FROM team_storage WHERE team = ?`,
		string(NormalizeTeam(team)))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := map[StorageKind]TeamStorageUsage{}
	for rows.Next() {
		var kind string
		var u TeamStorageUsage
		var reconciled int64
		if err := rows.Scan(&kind, &u.UsedBytes, &u.ReservedBytes, &reconciled); err != nil {
			return nil, err
		}
		if reconciled > 0 {
			u.ReconciledAt = time.Unix(0, reconciled).UTC()
		}
		out[StorageKind(kind)] = u
	}
	return out, rows.Err()
}

// StorageMarks reports every team's running total of committed bytes in
// kind. The storage pass takes it before it lists the bucket and hands it to
// [Store.ReconcileStorage], which keeps what was committed in between.
func (s *Store) StorageMarks(ctx context.Context, kind StorageKind) (_ map[Team]int64, err error) {
	rows, err := s.query(ctx, `SELECT team, committed_bytes FROM team_storage WHERE store = ?`, string(kind))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := map[Team]int64{}
	for rows.Next() {
		var team string
		var n int64
		if err := rows.Scan(&team, &n); err != nil {
			return nil, err
		}
		out[Team(team)] = n
	}
	return out, rows.Err()
}

// ReconcileStorage replaces every team's stored bytes in kind with what a
// listing found plus what was committed since marks was taken. A team the
// listing did not find holds only what it committed since. An object written
// while its team was being listed can land in both and count twice, which
// holds the team to less, never more, until the next pass. A listed team
// the deployment does not know gets no row.
func (s *Store) ReconcileStorage(ctx context.Context, kind StorageKind, listed, marks map[Team]int64, now time.Time) (err error) {
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown store %q", ErrInvalidInput, kind)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	for team := range listed {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO team_storage (team, store, used_bytes, reserved_bytes, committed_bytes, reconciled_at, updated_at)
SELECT ?, ?, 0, 0, 0, 0, ? WHERE EXISTS (SELECT 1 FROM teams WHERE name = ?)
ON CONFLICT (team, store) DO NOTHING`, string(team), string(kind), now.UnixNano(), string(team)); err != nil {
			return err
		}
	}
	committed, err := lockCommittedTx(ctx, tx, kind)
	if err != nil {
		return err
	}
	for team, total := range committed {
		used := max(listed[team]+total-marks[team], 0)
		if _, err := tx.ExecContext(ctx, `
UPDATE team_storage SET used_bytes = ?, reconciled_at = ?, updated_at = ? WHERE team = ? AND store = ?`,
			used, now.UnixNano(), now.UnixNano(), string(team), string(kind)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func lockCommittedTx(ctx context.Context, tx *storeTx, kind StorageKind) (_ map[Team]int64, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT team, committed_bytes FROM team_storage WHERE store = ? ORDER BY team`+tx.forUpdate(), string(kind))
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
	out := map[Team]int64{}
	for rows.Next() {
		var team string
		var n int64
		if err := rows.Scan(&team, &n); err != nil {
			return nil, err
		}
		out[Team(team)] = n
	}
	return out, rows.Err()
}

// DownloadCharge asks to serve a team's download.
type DownloadCharge struct {
	Team Team
	// Bytes is the download's size. Zero checks that the team has room
	// left today without charging it.
	Bytes int64
	// Record charges Bytes whatever the cap says, for a stream whose size
	// was learned only once it finished.
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
// [*DownloadCapError] when they would pass its cap. The operator's team has
// no cap. The row is locked for the charge, so readers racing for the last
// bytes of a day cannot both see room.
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
	if !c.Record && out.CapBytes > 0 && used+max(c.Bytes, 1) > out.CapBytes {
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

// PruneDownloadDays deletes every team's download days before day, which is
// spelled "2006-01-02".
func (s *Store) PruneDownloadDays(ctx context.Context, day string) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM team_download_day WHERE day < ?`, day)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// EgressTotals is what one service sent in a UTC day and in that day's
// month.
type EgressTotals struct {
	Service    string `json:"service"`
	Day        string `json:"day"`
	DayBytes   int64  `json:"day_bytes"`
	Month      string `json:"month"`
	MonthBytes int64  `json:"month_bytes"`
}

// RecordEgressTotals keeps the larger of each stored total and what t
// reports, and returns the stored totals. A restarted service records zeros
// to read back where its day and month stood.
func (s *Store) RecordEgressTotals(ctx context.Context, t EgressTotals) (_ EgressTotals, err error) {
	if t.Service == "" || t.Day == "" || t.Month == "" || t.DayBytes < 0 || t.MonthBytes < 0 {
		return EgressTotals{}, fmt.Errorf("%w: egress totals need a service, a day and a month", ErrInvalidInput)
	}
	now := time.Now().UnixNano()
	tx, err := s.beginTx(ctx)
	if err != nil {
		return EgressTotals{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	out := t
	for _, p := range []struct {
		period string
		bytes  *int64
	}{{t.Day, &out.DayBytes}, {t.Month, &out.MonthBytes}} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO egress_day (service, period, bytes, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT (service, period) DO UPDATE SET
    bytes = CASE WHEN excluded.bytes > egress_day.bytes THEN excluded.bytes ELSE egress_day.bytes END,
    updated_at = excluded.updated_at`, t.Service, p.period, *p.bytes, now); err != nil {
			return EgressTotals{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT bytes FROM egress_day WHERE service = ? AND period = ?`,
			t.Service, p.period).Scan(p.bytes); err != nil {
			return EgressTotals{}, err
		}
	}
	return out, tx.Commit()
}

// PruneEgressTotals deletes every service's totals for periods before
// month, which is spelled "2006-01"; a day of that month sorts after it.
func (s *Store) PruneEgressTotals(ctx context.Context, month string) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM egress_day WHERE period < ?`, month)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
