package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Event size limits, named so a refusal says which one it hit.
const (
	StorageLimitEventBytes       = "event_bytes"
	StorageLimitEventBytesPerRun = "event_bytes_per_run"
	StorageLimitEventsPerRun     = "events_per_run"
)

// MaxEventKindBytes bounds an event's kind. A kind names a category, so it
// is short and drawn from [A-Za-z0-9_.:-].
const MaxEventKindBytes = 128

// ErrInvalidEventKind is returned when an event's kind is empty, longer than
// [MaxEventKindBytes], or holds a byte outside [A-Za-z0-9_.:-].
var ErrInvalidEventKind = errors.New("invalid event kind")

// DefaultEventLimits bounds what a run can store as events when the
// operator sets nothing: 256 KiB per event, 64 MiB and 50,000 events per run.
var DefaultEventLimits = EventLimits{MaxBytesPerEvent: 256 << 10, MaxBytesPerRun: 64 << 20, MaxEventsPerRun: 50_000}

// EventLimits caps what a run stores as events. An event's bytes are its
// kind plus its payload. The caps hold whether or not a storage quota tier is
// configured, because an unconfigured controller is exactly the one with
// nothing else between a runner token and the database's disk. A value of
// zero or less lifts that cap.
type EventLimits struct {
	MaxBytesPerEvent int64
	MaxBytesPerRun   int64
	MaxEventsPerRun  int64
}

const (
	metaKeyEventMaxBytes        = "event_max_bytes"
	metaKeyEventMaxBytesPerRun  = "event_max_bytes_per_run"
	metaKeyEventMaxEventsPerRun = "event_max_events_per_run"
)

// ValidateEventKind reports [ErrInvalidEventKind] unless kind is 1 to
// [MaxEventKindBytes] bytes of [A-Za-z0-9_.:-].
func ValidateEventKind(kind string) error {
	if kind == "" || len(kind) > MaxEventKindBytes {
		return fmt.Errorf("%w: length %d, want 1 to %d bytes", ErrInvalidEventKind, len(kind), MaxEventKindBytes)
	}
	for i := 0; i < len(kind); i++ {
		c := kind[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '_', c == '.', c == ':', c == '-':
		default:
			return fmt.Errorf("%w: byte %q at %d is outside [A-Za-z0-9_.:-]", ErrInvalidEventKind, c, i)
		}
	}
	return nil
}

// eventBytes is what an event occupies against the byte caps.
func eventBytes(kind string, payload []byte) int64 {
	return int64(len(kind) + len(payload))
}

// EventLimits returns the caps in force, the defaults where the operator
// set none.
func (s *Store) EventLimits(ctx context.Context) (_ EventLimits, err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return EventLimits{}, err
	}
	defer rollbackUnlessDone(tx, &err)
	limits, err := eventLimitsTx(ctx, tx)
	if err != nil {
		return EventLimits{}, err
	}
	return limits, tx.Commit()
}

// SetEventLimits replaces every cap; zero or less lifts one.
func (s *Store) SetEventLimits(ctx context.Context, limits EventLimits) (err error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx, &err)
	if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxBytes, formatCreditSetting(limits.MaxBytesPerEvent)); err != nil {
		return err
	}
	if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxBytesPerRun, formatCreditSetting(limits.MaxBytesPerRun)); err != nil {
		return err
	}
	if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxEventsPerRun, formatCreditSetting(limits.MaxEventsPerRun)); err != nil {
		return err
	}
	return tx.Commit()
}

func eventLimitsTx(ctx context.Context, tx *storeTx) (EventLimits, error) {
	perEvent, err := creditSettingTx(ctx, tx, metaKeyEventMaxBytes, DefaultEventLimits.MaxBytesPerEvent)
	if err != nil {
		return EventLimits{}, err
	}
	perRun, err := creditSettingTx(ctx, tx, metaKeyEventMaxBytesPerRun, DefaultEventLimits.MaxBytesPerRun)
	if err != nil {
		return EventLimits{}, err
	}
	events, err := creditSettingTx(ctx, tx, metaKeyEventMaxEventsPerRun, DefaultEventLimits.MaxEventsPerRun)
	if err != nil {
		return EventLimits{}, err
	}
	return EventLimits{MaxBytesPerEvent: perEvent, MaxBytesPerRun: perRun, MaxEventsPerRun: events}, nil
}

// safety: the run's stored totals are read under the run's event-sequence
// lock, the one every append takes, so two appends cannot both read room
// that only one of them fits in. The totals are counters on the run row that
// every append bumps, so the check costs one row read however long the run.
func refuseEventOverLimitsTx(ctx context.Context, tx *storeTx, principal, runID string, size int64) error {
	limits, err := eventLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.MaxBytesPerEvent > 0 && size > limits.MaxBytesPerEvent {
		return &StorageQuotaError{
			Principal: principal, Limit: StorageLimitEventBytes, Unit: "bytes",
			Allowed: limits.MaxBytesPerEvent, Requested: size,
		}
	}
	if limits.MaxBytesPerRun <= 0 && limits.MaxEventsPerRun <= 0 {
		return nil
	}
	if err := lockEventSequenceTx(ctx, tx, runID); err != nil {
		return err
	}
	var storedBytes, storedEvents int64
	err = tx.QueryRowContext(ctx,
		`SELECT event_bytes, event_count FROM runs WHERE id = ?`, runID).Scan(&storedBytes, &storedEvents)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	if err != nil {
		return err
	}
	if limits.MaxBytesPerRun > 0 && storedBytes+size > limits.MaxBytesPerRun {
		return &StorageQuotaError{
			Principal: principal, Limit: StorageLimitEventBytesPerRun, Unit: "bytes",
			Used: storedBytes, Allowed: limits.MaxBytesPerRun, Requested: size,
		}
	}
	if limits.MaxEventsPerRun > 0 && storedEvents+1 > limits.MaxEventsPerRun {
		return &StorageQuotaError{
			Principal: principal, Limit: StorageLimitEventsPerRun, Unit: "events",
			Used: storedEvents, Allowed: limits.MaxEventsPerRun, Requested: 1,
		}
	}
	return nil
}

// runEventUsageCols count what each run has appended as events, so the
// per-run caps read one row instead of summing the run's events.
var runEventUsageCols = map[string]string{
	"event_bytes": "BIGINT NOT NULL DEFAULT 0",
	"event_count": "BIGINT NOT NULL DEFAULT 0",
}

func backfillRunEventUsageTx(ctx context.Context, tx *storeTx) error {
	_, err := tx.ExecContext(ctx, `
UPDATE runs SET
    event_bytes = (SELECT COALESCE(SUM(LENGTH(kind) + LENGTH(payload)), 0) FROM events WHERE events.run_id = runs.id),
    event_count = (SELECT COUNT(*) FROM events WHERE events.run_id = runs.id)`)
	return err
}
