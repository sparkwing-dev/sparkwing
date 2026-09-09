package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Overlap policies decide what a due instant does while the schedule's
// previous run is still going. Exported wire values.
const (
	CronOverlapSkip  = "skip"
	CronOverlapQueue = "queue"
)

// Outcomes a due instant resolves to. Exported wire values.
const (
	CronOutcomeFired          = "fired"
	CronOutcomeSkippedOverlap = "skipped_overlap"
	CronOutcomeMissed         = "missed"
	CronOutcomeFailed         = "failed"
)

// cronFireIDPrefix marks a minted fire id; the store mints one because
// neither dialect autoincrements the table's text primary key.
const cronFireIDPrefix = "crf_"

// maxCronFiresPerSchedule bounds the retained history so a minutely
// schedule cannot grow the table without limit.
const maxCronFiresPerSchedule = 200

// defaultCronFireLimit is what ListCronFires reads when the caller
// names no limit.
const defaultCronFireLimit = 50

// CronSchedule is one armed pipeline schedule. The declaration fields
// (Cron, TZ, Overlap, CatchUp) come from the repository's
// sparkwing.yaml; the rest is host state the evaluator advances.
// Declared goes false when the repository stops declaring it, which
// keeps the row and its fire history readable after the withdrawal.
type CronSchedule struct {
	ID       string `json:"id"`
	RepoPath string `json:"repo_path"`
	Pipeline string `json:"pipeline"`
	Cron     string `json:"cron"`
	TZ       string `json:"tz"`
	Overlap  string `json:"overlap"`
	// CatchUp is how far back a tick will still fire a missed instant.
	CatchUp   time.Duration `json:"catch_up"`
	Paused    bool          `json:"paused"`
	Declared  bool          `json:"declared"`
	ArmedAt   time.Time     `json:"armed_at"`
	ArmedBy   string        `json:"armed_by,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
	// CursorAt is the last due instant resolved, however it resolved,
	// so a later tick never reconsiders it.
	CursorAt    time.Time  `json:"cursor_at"`
	LastFiredAt *time.Time `json:"last_fired_at,omitempty"`
	LastRunID   string     `json:"last_run_id,omitempty"`
	LastOutcome string     `json:"last_outcome,omitempty"`
	// NextDueAt is nil when the expression never matches again.
	NextDueAt *time.Time `json:"next_due_at,omitempty"`
}

// CronFire is one resolved due instant. RunID is set only for
// CronOutcomeFired; Detail carries the reason for the other outcomes.
type CronFire struct {
	ID         string    `json:"id"`
	ScheduleID string    `json:"schedule_id"`
	DueAt      time.Time `json:"due_at"`
	DecidedAt  time.Time `json:"decided_at"`
	Outcome    string    `json:"outcome"`
	RunID      string    `json:"run_id,omitempty"`
	Detail     string    `json:"detail,omitempty"`
}

// CronTick is the last OS tick this store saw. A zero At means no tick
// has ever been recorded.
type CronTick struct {
	At      time.Time `json:"at"`
	Host    string    `json:"host,omitempty"`
	Version string    `json:"version,omitempty"`
	Error   string    `json:"error,omitempty"`
}

const (
	metaKeyCronLastTickAt      = "crons.last_tick_at"
	metaKeyCronLastTickHost    = "crons.last_tick_host"
	metaKeyCronLastTickVersion = "crons.last_tick_version"
	metaKeyCronLastTickError   = "crons.last_tick_error"
)

const cronScheduleColumns = `id, repo_path, pipeline, cron, tz, overlap, catch_up_ns, paused, declared,
       armed_at, armed_by, updated_at, cursor_at, last_fired_at, last_run_id, last_outcome, next_due_at`

const cronFireColumns = `id, schedule_id, due_at, decided_at, outcome, run_id, detail`

// ArmCronSchedule records the declaration on a host, returning the
// stored row and whether it was created. An existing row keeps its id,
// arming stamp, pause state, cursor, and last-fire fields: re-arming
// republishes what the repository declares and re-marks the schedule
// declared, it does not restart its history. ArmedAt defaults to now
// on a create when the caller leaves it zero, and an empty Overlap
// means CronOverlapSkip.
func (s *Store) ArmCronSchedule(ctx context.Context, sched CronSchedule, now time.Time) (CronSchedule, bool, error) {
	if sched.ID == "" || sched.RepoPath == "" || sched.Pipeline == "" {
		return CronSchedule{}, false, fmt.Errorf("ArmCronSchedule: id, repo_path and pipeline required")
	}
	if sched.Overlap == "" {
		sched.Overlap = CronOverlapSkip
	}
	if sched.Overlap != CronOverlapSkip && sched.Overlap != CronOverlapQueue {
		return CronSchedule{}, false, fmt.Errorf("ArmCronSchedule: unknown overlap policy %q", sched.Overlap)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return CronSchedule{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM cron_schedules WHERE repo_path = ? AND pipeline = ?`+tx.forUpdate(),
		sched.RepoPath, sched.Pipeline).Scan(&id)
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return CronSchedule{}, false, err
	}
	if created {
		id = sched.ID
		armedAt := sched.ArmedAt
		if armedAt.IsZero() {
			armedAt = now
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO cron_schedules (`+cronScheduleColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, sched.RepoPath, sched.Pipeline, sched.Cron, sched.TZ, sched.Overlap,
			int64(sched.CatchUp), boolToInt(sched.Paused), 1,
			armedAt.UnixNano(), sched.ArmedBy, now.UnixNano(), now.UnixNano(),
			nullNanos(sched.LastFiredAt), sched.LastRunID, sched.LastOutcome,
			nullNanos(sched.NextDueAt),
		); err != nil {
			return CronSchedule{}, false, err
		}
	} else if _, err := tx.ExecContext(ctx, `
UPDATE cron_schedules
   SET cron = ?, tz = ?, overlap = ?, catch_up_ns = ?, declared = 1,
       updated_at = ?, next_due_at = ?
 WHERE id = ?`,
		sched.Cron, sched.TZ, sched.Overlap, int64(sched.CatchUp),
		now.UnixNano(), nullNanos(sched.NextDueAt), id,
	); err != nil {
		return CronSchedule{}, false, err
	}
	stored, err := getCronScheduleTx(ctx, tx, id)
	if err != nil {
		return CronSchedule{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CronSchedule{}, false, err
	}
	return stored, created, nil
}

// ListCronSchedules returns every schedule armed on this host, ordered
// by repository path then pipeline.
func (s *Store) ListCronSchedules(ctx context.Context) ([]CronSchedule, error) {
	rows, err := s.query(ctx, `SELECT `+cronScheduleColumns+`
  FROM cron_schedules
 ORDER BY repo_path, pipeline`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CronSchedule
	for rows.Next() {
		sched, err := scanCronSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sched)
	}
	return out, rows.Err()
}

// GetCronSchedule returns one schedule, or an error wrapping
// [ErrNotFound] when the id is unknown.
func (s *Store) GetCronSchedule(ctx context.Context, id string) (CronSchedule, error) {
	sched, err := scanCronSchedule(s.queryRow(ctx,
		`SELECT `+cronScheduleColumns+` FROM cron_schedules WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return CronSchedule{}, notFound("cron schedule", id)
	}
	return sched, err
}

// SetCronSchedulePaused pauses or resumes a schedule. A paused
// schedule still advances its cursor, so resuming does not replay the
// instants that passed while it was paused.
func (s *Store) SetCronSchedulePaused(ctx context.Context, id string, paused bool, now time.Time) error {
	return s.updateCronSchedule(ctx, id,
		`UPDATE cron_schedules SET paused = ?, updated_at = ? WHERE id = ?`,
		boolToInt(paused), now.UnixNano(), id)
}

// SetCronScheduleDeclared records whether the repository still
// declares this schedule.
func (s *Store) SetCronScheduleDeclared(ctx context.Context, id string, declared bool, now time.Time) error {
	return s.updateCronSchedule(ctx, id,
		`UPDATE cron_schedules SET declared = ?, updated_at = ? WHERE id = ?`,
		boolToInt(declared), now.UnixNano(), id)
}

// SetCronScheduleNextDue republishes the next matching instant, nil
// when the expression never matches again.
func (s *Store) SetCronScheduleNextDue(ctx context.Context, id string, next *time.Time, now time.Time) error {
	return s.updateCronSchedule(ctx, id,
		`UPDATE cron_schedules SET next_due_at = ?, updated_at = ? WHERE id = ?`,
		nullNanos(next), now.UnixNano(), id)
}

// DeleteCronSchedulesForRepo disarms every schedule of one repository
// checkout and returns how many rows went, so a caller can report an
// uninstall that found nothing.
func (s *Store) DeleteCronSchedulesForRepo(ctx context.Context, repoPath string) (int, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM cron_fires
          WHERE schedule_id IN (SELECT id FROM cron_schedules WHERE repo_path = ?)`, repoPath); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM cron_schedules WHERE repo_path = ?`, repoPath)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}

// ResolveCronDue advances the schedule past one due instant: the cursor
// moves forward to cursor, the next match to next, and fire, when given,
// joins the schedule's history. The cursor only ever moves forward, so a
// caller holding an older reading cannot rewind one a concurrent tick
// already advanced. The cursor and the history move together so a tick
// that dies between them cannot fire the same instant twice.
// A fired outcome also stamps last_fired_at, last_run_id and
// last_outcome; every other outcome stamps last_outcome alone, leaving
// the last successful launch on the row. fire.ID is minted when empty.
func (s *Store) ResolveCronDue(ctx context.Context, id string, cursor time.Time, next *time.Time, fire *CronFire, now time.Time) error {
	if fire != nil && fire.Outcome == "" {
		return fmt.Errorf("ResolveCronDue: fire outcome required")
	}
	if fire != nil && !knownCronOutcome(fire.Outcome) {
		return fmt.Errorf("ResolveCronDue: unknown outcome %q", fire.Outcome)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var exists string
	switch err := tx.QueryRowContext(ctx,
		`SELECT id FROM cron_schedules WHERE id = ?`+tx.forUpdate(), id).Scan(&exists); {
	case errors.Is(err, sql.ErrNoRows):
		return notFound("cron schedule", id)
	case err != nil:
		return err
	}

	// safety: `crons run` resolves outside the tick lock, so a stale cursor must
	// never pull a concurrent tick's newer one backwards onto instants it
	// already resolved.
	advance := "cursor_at = " + s.greatest() + "(cursor_at, ?)"
	update := `UPDATE cron_schedules SET ` + advance + `, next_due_at = ?, updated_at = ? WHERE id = ?`
	args := []any{cursor.UnixNano(), nullNanos(next), now.UnixNano(), id}
	if fire != nil {
		if fire.Outcome == CronOutcomeFired {
			update = `UPDATE cron_schedules
                         SET ` + advance + `, next_due_at = ?, updated_at = ?,
                             last_fired_at = ?, last_run_id = ?, last_outcome = ?
                       WHERE id = ?`
			args = []any{cursor.UnixNano(), nullNanos(next), now.UnixNano(),
				fire.DecidedAt.UnixNano(), fire.RunID, fire.Outcome, id}
		} else {
			update = `UPDATE cron_schedules
                         SET ` + advance + `, next_due_at = ?, updated_at = ?, last_outcome = ?
                       WHERE id = ?`
			args = []any{cursor.UnixNano(), nullNanos(next), now.UnixNano(), fire.Outcome, id}
		}
	}
	if _, err := tx.ExecContext(ctx, update, args...); err != nil {
		return err
	}
	if fire != nil {
		fireID := fire.ID
		if fireID == "" {
			fireID, err = newOpaqueIdentity(cronFireIDPrefix)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO cron_fires (`+cronFireColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			fireID, id, fire.DueAt.UnixNano(), fire.DecidedAt.UnixNano(),
			fire.Outcome, fire.RunID, fire.Detail); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM cron_fires
              WHERE schedule_id = ?
                AND id NOT IN (SELECT id FROM cron_fires
                                WHERE schedule_id = ?
                                ORDER BY decided_at DESC, id DESC
                                LIMIT ?)`,
			id, id, maxCronFiresPerSchedule); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListCronFires returns a schedule's resolved instants newest first.
// A limit of zero or less reads the newest 50.
func (s *Store) ListCronFires(ctx context.Context, scheduleID string, limit int) ([]CronFire, error) {
	if limit <= 0 {
		limit = defaultCronFireLimit
	}
	rows, err := s.query(ctx, `SELECT `+cronFireColumns+`
  FROM cron_fires
 WHERE schedule_id = ?
 ORDER BY decided_at DESC, id DESC
 LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CronFire
	for rows.Next() {
		var fire CronFire
		var dueNS, decidedNS int64
		if err := rows.Scan(&fire.ID, &fire.ScheduleID, &dueNS, &decidedNS,
			&fire.Outcome, &fire.RunID, &fire.Detail); err != nil {
			return nil, err
		}
		fire.DueAt = time.Unix(0, dueNS)
		fire.DecidedAt = time.Unix(0, decidedNS)
		out = append(out, fire)
	}
	return out, rows.Err()
}

// RecordCronTick stamps the OS tick that just ran, so an operator can
// tell a host whose timer stopped from one whose schedules are simply
// not due. Error is the tick's own failure, empty on success.
func (s *Store) RecordCronTick(ctx context.Context, tick CronTick) error {
	atNS := int64(0)
	if !tick.At.IsZero() {
		atNS = tick.At.UnixNano()
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, kv := range [][2]string{
		{metaKeyCronLastTickAt, strconv.FormatInt(atNS, 10)},
		{metaKeyCronLastTickHost, tick.Host},
		{metaKeyCronLastTickVersion, tick.Version},
		{metaKeyCronLastTickError, tick.Error},
	} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES (?, ?, ?)
			 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			kv[0], kv[1], atNS); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetCronTick returns the last recorded tick, zero-valued when this
// host has never ticked.
func (s *Store) GetCronTick(ctx context.Context) (CronTick, error) {
	rows, err := s.query(ctx, `SELECT key, value FROM sparkwing_meta WHERE key IN (?, ?, ?, ?)`,
		metaKeyCronLastTickAt, metaKeyCronLastTickHost,
		metaKeyCronLastTickVersion, metaKeyCronLastTickError)
	if err != nil {
		return CronTick{}, err
	}
	defer func() { _ = rows.Close() }()
	var tick CronTick
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return CronTick{}, err
		}
		switch key {
		case metaKeyCronLastTickAt:
			ns, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return CronTick{}, fmt.Errorf("parse %s %q: %w", key, value, err)
			}
			if ns != 0 {
				tick.At = time.Unix(0, ns)
			}
		case metaKeyCronLastTickHost:
			tick.Host = value
		case metaKeyCronLastTickVersion:
			tick.Version = value
		case metaKeyCronLastTickError:
			tick.Error = value
		}
	}
	return tick, rows.Err()
}

func (s *Store) updateCronSchedule(ctx context.Context, id, query string, args ...any) error {
	res, err := s.exec(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound("cron schedule", id)
	}
	return nil
}

func getCronScheduleTx(ctx context.Context, tx *storeTx, id string) (CronSchedule, error) {
	sched, err := scanCronSchedule(tx.QueryRowContext(ctx,
		`SELECT `+cronScheduleColumns+` FROM cron_schedules WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return CronSchedule{}, notFound("cron schedule", id)
	}
	return sched, err
}

func scanCronSchedule(scan func(...any) error) (CronSchedule, error) {
	var sched CronSchedule
	var catchUpNS, armedNS, updatedNS, cursorNS int64
	var paused, declared int64
	var lastFiredNS, nextDueNS sql.NullInt64
	if err := scan(
		&sched.ID, &sched.RepoPath, &sched.Pipeline, &sched.Cron, &sched.TZ, &sched.Overlap,
		&catchUpNS, &paused, &declared,
		&armedNS, &sched.ArmedBy, &updatedNS, &cursorNS,
		&lastFiredNS, &sched.LastRunID, &sched.LastOutcome, &nextDueNS,
	); err != nil {
		return CronSchedule{}, err
	}
	sched.CatchUp = time.Duration(catchUpNS)
	sched.Paused = paused != 0
	sched.Declared = declared != 0
	sched.ArmedAt = time.Unix(0, armedNS)
	sched.UpdatedAt = time.Unix(0, updatedNS)
	sched.CursorAt = time.Unix(0, cursorNS)
	sched.LastFiredAt = nanosToTime(lastFiredNS)
	sched.NextDueAt = nanosToTime(nextDueNS)
	return sched, nil
}

func knownCronOutcome(outcome string) bool {
	switch outcome {
	case CronOutcomeFired, CronOutcomeSkippedOverlap, CronOutcomeMissed, CronOutcomeFailed:
		return true
	}
	return false
}

func nullNanos(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixNano()
}

func nanosToTime(ns sql.NullInt64) *time.Time {
	if !ns.Valid {
		return nil
	}
	at := time.Unix(0, ns.Int64)
	return &at
}
