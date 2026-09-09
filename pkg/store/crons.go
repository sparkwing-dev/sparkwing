package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// Where a schedule's due instants are evaluated and launched. Exported
// wire values.
const (
	CronWhereLocal      = "local"
	CronWhereController = "controller"
)

// CronScheduleDefaultName is the name a repository's lone schedule for a
// pipeline carries, so a pipeline that declares one cadence needs no name
// anywhere.
const CronScheduleDefaultName = "default"

// safety: the store mints fire ids because neither dialect autoincrements a text primary key.
const cronFireIDPrefix = "crf_"

// safety: bounds retained history so a minutely schedule cannot grow the table without limit.
const maxCronFiresPerSchedule = 200

const defaultCronFireLimit = 50

// CronSchedule is one armed pipeline schedule. The declaration fields
// (Cron, TZ, Overlap, CatchUp, Where, Args and the lock) come from the
// repository's sparkwing.yaml and the arming host; the rest is host
// state the evaluator advances. Declared goes false when the repository
// stops declaring it, which keeps the row and its fire history readable
// after the withdrawal. What actually runs is [CronSchedule.Effective],
// which lays Override over the declaration.
type CronSchedule struct {
	ID       string `json:"id"`
	RepoPath string `json:"repo_path"`
	Pipeline string `json:"pipeline"`
	// Name distinguishes several schedules of one pipeline;
	// [CronScheduleDefaultName] when the pipeline declares one.
	Name    string `json:"name"`
	Cron    string `json:"cron"`
	TZ      string `json:"tz"`
	Overlap string `json:"overlap"`
	// CatchUp is how far back a tick will still fire a missed instant.
	CatchUp time.Duration `json:"catch_up"`
	// Where is CronWhereLocal or CronWhereController.
	Where string `json:"where"`
	// GitBranch is the branch a controller schedule was pushed from,
	// empty for one a host arms from a working tree.
	GitBranch string `json:"git_branch,omitempty"`
	// Args are the CLI arguments the scheduled launch passes, keyed by
	// flag name.
	Args map[string]string `json:"args,omitempty"`
	// LockedRef is the commit the schedule is pinned to, empty when it
	// follows the checkout. LockedBinary and LockedDigest name the
	// pipeline binary that pin resolved to and the cache digest it was
	// built from.
	LockedRef    string `json:"locked_ref,omitempty"`
	LockedBinary string `json:"locked_binary,omitempty"`
	LockedDigest string `json:"locked_digest,omitempty"`
	// Override is the host's edit of the declaration, nil when the
	// schedule runs what the repository declares.
	Override  *CronOverride `json:"override,omitempty"`
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

// CronDeclaration is the cadence a schedule runs: what the repository
// declared, or that with the host's override laid over it.
type CronDeclaration struct {
	Cron         string            `json:"cron"`
	TZ           string            `json:"tz"`
	Overlap      string            `json:"overlap"`
	CatchUp      time.Duration     `json:"catch_up"`
	Where        string            `json:"where"`
	GitBranch    string            `json:"git_branch,omitempty"`
	Args         map[string]string `json:"args,omitempty"`
	LockedRef    string            `json:"locked_ref,omitempty"`
	LockedBinary string            `json:"locked_binary,omitempty"`
	LockedDigest string            `json:"locked_digest,omitempty"`
}

// CronOverride is a host's edit of a declared schedule. An empty Cron,
// TZ or Overlap and a nil CatchUp leave that field to the declaration;
// a nil Args does the same, while an empty non-nil Args overrides the
// declaration to no arguments at all. Base is the declaration the
// override was set against, so a reader can tell an override whose
// declaration has moved under it since.
type CronOverride struct {
	Cron    string            `json:"cron,omitempty"`
	TZ      string            `json:"tz,omitempty"`
	Overlap string            `json:"overlap,omitempty"`
	CatchUp *time.Duration    `json:"catch_up,omitempty"`
	Args    map[string]string `json:"args,omitempty"`
	Base    CronDeclaration   `json:"base"`
	SetAt   time.Time         `json:"set_at"`
}

// CronLock pins a schedule to one commit and the pipeline binary built
// from it. A zero CronLock unlocks the schedule, which then follows the
// checkout.
type CronLock struct {
	Ref    string `json:"ref,omitempty"`
	Binary string `json:"binary,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// Declaration returns the cadence the repository declared, before any
// host override.
func (s CronSchedule) Declaration() CronDeclaration {
	return CronDeclaration{
		Cron:         s.Cron,
		TZ:           s.TZ,
		Overlap:      s.Overlap,
		CatchUp:      s.CatchUp,
		Where:        s.Where,
		GitBranch:    s.GitBranch,
		Args:         s.Args,
		LockedRef:    s.LockedRef,
		LockedBinary: s.LockedBinary,
		LockedDigest: s.LockedDigest,
	}
}

// Effective returns the cadence this schedule actually runs: the
// declaration with every overridden field replaced. Where and the lock
// are not overridable, so they come from the declaration either way.
func (s CronSchedule) Effective() CronDeclaration {
	decl := s.Declaration()
	if s.Override == nil {
		return decl
	}
	if s.Override.Cron != "" {
		decl.Cron = s.Override.Cron
	}
	if s.Override.TZ != "" {
		decl.TZ = s.Override.TZ
	}
	if s.Override.Overlap != "" {
		decl.Overlap = s.Override.Overlap
	}
	if s.Override.CatchUp != nil {
		decl.CatchUp = *s.Override.CatchUp
	}
	if s.Override.Args != nil {
		decl.Args = s.Override.Args
	}
	return decl
}

// CronFire is one resolved due instant. RunID is set only for
// CronOutcomeFired; Detail carries the reason for the other outcomes,
// and Args the arguments the launch was given.
type CronFire struct {
	ID         string            `json:"id"`
	ScheduleID string            `json:"schedule_id"`
	DueAt      time.Time         `json:"due_at"`
	DecidedAt  time.Time         `json:"decided_at"`
	Outcome    string            `json:"outcome"`
	RunID      string            `json:"run_id,omitempty"`
	Detail     string            `json:"detail,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
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

const cronScheduleInsertColumns = `id, repo_path, pipeline, schedule_name, cron, tz, overlap, catch_up_ns,
       where_, git_branch, args, locked_ref, locked_binary, locked_digest, paused, declared,
       armed_at, armed_by, updated_at, cursor_at, last_fired_at, last_run_id, last_outcome, next_due_at`

const cronScheduleOverrideColumns = `override_cron, override_tz, override_overlap, override_catch_up_ns,
       override_args, override_base, override_set_at`

const cronScheduleColumns = cronScheduleInsertColumns + `,
       ` + cronScheduleOverrideColumns

var cronScheduleInsertPlaceholders = placeholders(strings.Count(cronScheduleInsertColumns, ",") + 1)

const cronFireColumns = `id, schedule_id, due_at, decided_at, outcome, run_id, detail, args`

// ArmCronSchedule records the declaration on a host, returning the
// stored row and whether it was created. A schedule is keyed by
// repository path, pipeline and name, so one pipeline can carry several
// cadences. An existing row keeps its id, arming stamp, pause state,
// cursor, last-fire fields and override: re-arming republishes what the
// repository declares and re-marks the schedule declared, it does not
// restart its history or discard the host's edits. ArmedAt defaults to
// now on a create when the caller leaves it zero, an empty Name means
// [CronScheduleDefaultName], an empty Overlap means CronOverlapSkip, and
// an empty Where means CronWhereLocal.
func (s *Store) ArmCronSchedule(ctx context.Context, sched CronSchedule, now time.Time) (stored CronSchedule, created bool, err error) {
	if sched.ID == "" || sched.RepoPath == "" || sched.Pipeline == "" {
		return CronSchedule{}, false, fmt.Errorf("ArmCronSchedule: id, repo_path and pipeline required")
	}
	if sched.Name == "" {
		sched.Name = CronScheduleDefaultName
	}
	if sched.Overlap == "" {
		sched.Overlap = CronOverlapSkip
	}
	if sched.Overlap != CronOverlapSkip && sched.Overlap != CronOverlapQueue {
		return CronSchedule{}, false, fmt.Errorf("ArmCronSchedule: unknown overlap policy %q", sched.Overlap)
	}
	if sched.Where == "" {
		sched.Where = CronWhereLocal
	}
	if sched.Where != CronWhereLocal && sched.Where != CronWhereController {
		return CronSchedule{}, false, fmt.Errorf("ArmCronSchedule: unknown where %q", sched.Where)
	}
	args, err := encodeCronArgs(sched.Args)
	if err != nil {
		return CronSchedule{}, false, fmt.Errorf("ArmCronSchedule: %w", err)
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return CronSchedule{}, false, err
	}
	defer rollbackUnlessDone(tx, &err)

	var id string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM cron_schedules
          WHERE repo_path = ? AND pipeline = ? AND schedule_name = ?`+tx.forUpdate(),
		sched.RepoPath, sched.Pipeline, sched.Name).Scan(&id)
	created = errors.Is(err, sql.ErrNoRows)
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
INSERT INTO cron_schedules (`+cronScheduleInsertColumns+`)
VALUES (`+cronScheduleInsertPlaceholders+`)`,
			id, sched.RepoPath, sched.Pipeline, sched.Name, sched.Cron, sched.TZ, sched.Overlap,
			int64(sched.CatchUp), sched.Where, sched.GitBranch, args,
			sched.LockedRef, sched.LockedBinary, sched.LockedDigest,
			boolToInt(sched.Paused), 1,
			armedAt.UnixNano(), sched.ArmedBy, now.UnixNano(), now.UnixNano(),
			nullNanos(sched.LastFiredAt), sched.LastRunID, sched.LastOutcome,
			nullNanos(sched.NextDueAt),
		); err != nil {
			return CronSchedule{}, false, err
		}
	} else if _, err := tx.ExecContext(ctx, `
UPDATE cron_schedules
   SET cron = ?, tz = ?, overlap = ?, catch_up_ns = ?, where_ = ?, git_branch = ?, args = ?,
       locked_ref = ?, locked_binary = ?, locked_digest = ?, declared = 1,
       updated_at = ?, next_due_at = ?
 WHERE id = ?`,
		sched.Cron, sched.TZ, sched.Overlap, int64(sched.CatchUp), sched.Where, sched.GitBranch, args,
		sched.LockedRef, sched.LockedBinary, sched.LockedDigest,
		now.UnixNano(), nullNanos(sched.NextDueAt), id,
	); err != nil {
		return CronSchedule{}, false, err
	}
	stored, err = getCronScheduleTx(ctx, tx, id)
	if err != nil {
		return CronSchedule{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CronSchedule{}, false, err
	}
	return stored, created, nil
}

// ListCronSchedules returns every schedule armed on this host, ordered
// by repository path, pipeline, then schedule name.
func (s *Store) ListCronSchedules(ctx context.Context) (out []CronSchedule, err error) {
	rows, err := s.query(ctx, `SELECT `+cronScheduleColumns+`
  FROM cron_schedules
 ORDER BY repo_path, pipeline, schedule_name`)
	if err != nil {
		return nil, err
	}
	defer closeRowsInto(rows, &err)
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

// SetCronScheduleLock pins a schedule to a commit and the pipeline
// binary built from it, so an updated checkout cannot change what an
// unattended run executes. A zero [CronLock] unlocks the schedule,
// which then follows the checkout again.
func (s *Store) SetCronScheduleLock(ctx context.Context, id string, lock CronLock, now time.Time) error {
	return s.updateCronSchedule(ctx, id,
		`UPDATE cron_schedules
            SET locked_ref = ?, locked_binary = ?, locked_digest = ?, updated_at = ?
          WHERE id = ?`,
		lock.Ref, lock.Binary, lock.Digest, now.UnixNano(), id)
}

// SetCronOverride replaces this host's edit of the declaration whole:
// every field the override leaves unset returns to what the repository
// declares. Base and SetAt come from the argument, and SetAt defaults
// to now when it is zero.
func (s *Store) SetCronOverride(ctx context.Context, id string, o CronOverride, now time.Time) error {
	if o.Overlap != "" && o.Overlap != CronOverlapSkip && o.Overlap != CronOverlapQueue {
		return fmt.Errorf("SetCronOverride: unknown overlap policy %q", o.Overlap)
	}
	var overrideArgs any
	if o.Args != nil {
		encoded, err := encodeCronArgs(o.Args)
		if err != nil {
			return fmt.Errorf("SetCronOverride: %w", err)
		}
		overrideArgs = encoded
	}
	base, err := json.Marshal(o.Base)
	if err != nil {
		return fmt.Errorf("SetCronOverride: encode base declaration: %w", err)
	}
	setAt := o.SetAt
	if setAt.IsZero() {
		setAt = now
	}
	var catchUp any
	if o.CatchUp != nil {
		catchUp = int64(*o.CatchUp)
	}
	return s.updateCronSchedule(ctx, id,
		`UPDATE cron_schedules
            SET override_cron = ?, override_tz = ?, override_overlap = ?,
                override_catch_up_ns = ?, override_args = ?, override_base = ?,
                override_set_at = ?, updated_at = ?
          WHERE id = ?`,
		o.Cron, o.TZ, o.Overlap, catchUp, overrideArgs, string(base),
		setAt.UnixNano(), now.UnixNano(), id)
}

// ClearCronOverride drops this host's edit, returning the schedule to
// what the repository declares.
func (s *Store) ClearCronOverride(ctx context.Context, id string, now time.Time) error {
	return s.updateCronSchedule(ctx, id,
		`UPDATE cron_schedules
            SET override_cron = NULL, override_tz = NULL, override_overlap = NULL,
                override_catch_up_ns = NULL, override_args = NULL, override_base = NULL,
                override_set_at = NULL, updated_at = ?
          WHERE id = ?`,
		now.UnixNano(), id)
}

// DeleteCronSchedule disarms one schedule and drops its fire history,
// leaving every sibling schedule of the same pipeline armed. An unknown
// id is an error wrapping [ErrNotFound].
func (s *Store) DeleteCronSchedule(ctx context.Context, id string) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if _, err := tx.ExecContext(ctx, `DELETE FROM cron_fires WHERE schedule_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM cron_schedules WHERE id = ?`, id)
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
	return tx.Commit()
}

// DeleteCronSchedulesForRepo disarms every schedule of one repository
// checkout and returns how many rows went, so a caller can report an
// uninstall that found nothing.
func (s *Store) DeleteCronSchedulesForRepo(ctx context.Context, repoPath string) (n int, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer rollbackUnlessDone(tx, &err)
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
func (s *Store) ResolveCronDue(ctx context.Context, id string, cursor time.Time, next *time.Time, fire *CronFire, now time.Time) (err error) {
	if fire != nil && fire.Outcome == "" {
		return fmt.Errorf("ResolveCronDue: fire outcome required")
	}
	if fire != nil && !knownCronOutcome(fire.Outcome) {
		return fmt.Errorf("ResolveCronDue: unknown outcome %q", fire.Outcome)
	}
	fireArgs := "{}"
	if fire != nil {
		if fireArgs, err = encodeCronArgs(fire.Args); err != nil {
			return fmt.Errorf("ResolveCronDue: %w", err)
		}
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)

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
			args = []any{
				cursor.UnixNano(), nullNanos(next), now.UnixNano(),
				fire.DecidedAt.UnixNano(), fire.RunID, fire.Outcome, id,
			}
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
			`INSERT INTO cron_fires (`+cronFireColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fireID, id, fire.DueAt.UnixNano(), fire.DecidedAt.UnixNano(),
			fire.Outcome, fire.RunID, fire.Detail, fireArgs); err != nil {
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
func (s *Store) ListCronFires(ctx context.Context, scheduleID string, limit int) (out []CronFire, err error) {
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
	defer closeRowsInto(rows, &err)
	for rows.Next() {
		var fire CronFire
		var dueNS, decidedNS int64
		var args string
		if err := rows.Scan(&fire.ID, &fire.ScheduleID, &dueNS, &decidedNS,
			&fire.Outcome, &fire.RunID, &fire.Detail, &args); err != nil {
			return nil, err
		}
		fire.DueAt = time.Unix(0, dueNS)
		fire.DecidedAt = time.Unix(0, decidedNS)
		if fire.Args, err = decodeCronArgs(args); err != nil {
			return nil, fmt.Errorf("cron fire %s: %w", fire.ID, err)
		}
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
	defer rollbackUnlessDone(tx, &err)
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
func (s *Store) GetCronTick(ctx context.Context) (tick CronTick, err error) {
	rows, err := s.query(ctx, `SELECT key, value FROM sparkwing_meta WHERE key IN (?, ?, ?, ?)`,
		metaKeyCronLastTickAt, metaKeyCronLastTickHost,
		metaKeyCronLastTickVersion, metaKeyCronLastTickError)
	if err != nil {
		return CronTick{}, err
	}
	defer closeRowsInto(rows, &err)
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
	var args string
	var lastFiredNS, nextDueNS, overrideCatchUpNS, overrideSetNS sql.NullInt64
	var overrideCron, overrideTZ, overrideOverlap, overrideArgs, overrideBase sql.NullString
	if err := scan(
		&sched.ID, &sched.RepoPath, &sched.Pipeline, &sched.Name,
		&sched.Cron, &sched.TZ, &sched.Overlap, &catchUpNS, &sched.Where, &sched.GitBranch, &args,
		&sched.LockedRef, &sched.LockedBinary, &sched.LockedDigest,
		&paused, &declared,
		&armedNS, &sched.ArmedBy, &updatedNS, &cursorNS,
		&lastFiredNS, &sched.LastRunID, &sched.LastOutcome, &nextDueNS,
		&overrideCron, &overrideTZ, &overrideOverlap, &overrideCatchUpNS,
		&overrideArgs, &overrideBase, &overrideSetNS,
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
	var err error
	if sched.Args, err = decodeCronArgs(args); err != nil {
		return CronSchedule{}, fmt.Errorf("cron schedule %s: %w", sched.ID, err)
	}
	if !overrideSetNS.Valid {
		return sched, nil
	}
	override := CronOverride{
		Cron:    overrideCron.String,
		TZ:      overrideTZ.String,
		Overlap: overrideOverlap.String,
		SetAt:   time.Unix(0, overrideSetNS.Int64),
	}
	if overrideCatchUpNS.Valid {
		catchUp := time.Duration(overrideCatchUpNS.Int64)
		override.CatchUp = &catchUp
	}
	// safety: an override to no arguments at all is a stored '{}', which has to
	// read back as an empty map rather than the nil that means "not overridden".
	if overrideArgs.Valid {
		override.Args = map[string]string{}
		if err := json.Unmarshal([]byte(overrideArgs.String), &override.Args); err != nil {
			return CronSchedule{}, fmt.Errorf("cron schedule %s: decode override args: %w", sched.ID, err)
		}
	}
	if overrideBase.Valid && overrideBase.String != "" {
		if err := json.Unmarshal([]byte(overrideBase.String), &override.Base); err != nil {
			return CronSchedule{}, fmt.Errorf("cron schedule %s: decode override base: %w", sched.ID, err)
		}
	}
	sched.Override = &override
	return sched, nil
}

// safety: encoding/json sorts map keys, so two equal argument sets always
// store the same bytes and compare equal wherever they are read back.
func encodeCronArgs(args map[string]string) (string, error) {
	if len(args) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("encode args: %w", err)
	}
	return string(encoded), nil
}

func decodeCronArgs(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	args := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("decode args: %w", err)
	}
	if len(args) == 0 {
		return nil, nil
	}
	return args, nil
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

// rollbackUnlessDone rolls a transaction back on the way out; a transaction
// already committed reports ErrTxDone, which is the expected case and dropped,
// while any other rollback failure becomes the function's error.
func rollbackUnlessDone(tx *storeTx, err *error) {
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) && *err == nil {
		*err = rerr
	}
}

// closeRowsInto closes a result set on the way out and surfaces a close
// failure as the function's error when nothing else already failed.
func closeRowsInto(rows *sql.Rows, err *error) {
	if cerr := rows.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}
