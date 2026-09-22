package store

import (
	"context"
)

// Event size limits, named so a refusal says which one it hit.
const (
	StorageLimitEventBytes       = "event_bytes"
	StorageLimitEventBytesPerRun = "event_bytes_per_run"
)

// DefaultEventLimits bounds what a run can store as events when the
// operator sets nothing: 256 KiB per event and 64 MiB per run.
var DefaultEventLimits = EventLimits{MaxBytesPerEvent: 256 << 10, MaxBytesPerRun: 64 << 20}

// EventLimits caps event payloads. They hold whether or not a storage quota
// tier is configured, because an unconfigured controller is exactly the one
// with nothing else between a runner token and the database's disk. A value
// of zero or less lifts that cap.
type EventLimits struct {
	MaxBytesPerEvent int64
	MaxBytesPerRun   int64
}

const (
	metaKeyEventMaxBytes       = "event_max_bytes"
	metaKeyEventMaxBytesPerRun = "event_max_bytes_per_run"
)

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

// SetEventLimits replaces both caps; zero or less lifts one.
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
	return EventLimits{MaxBytesPerEvent: perEvent, MaxBytesPerRun: perRun}, nil
}

// safety: the run's stored bytes are read under the run's event-sequence
// lock, the one every append takes, so two appends cannot both read room
// that only one of them fits in.
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
	if limits.MaxBytesPerRun <= 0 {
		return nil
	}
	if err := lockEventSequenceTx(ctx, tx, runID); err != nil {
		return err
	}
	var stored int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(LENGTH(payload)), 0) FROM events WHERE run_id = ?`, runID).Scan(&stored); err != nil {
		return err
	}
	if stored+size > limits.MaxBytesPerRun {
		return &StorageQuotaError{
			Principal: principal, Limit: StorageLimitEventBytesPerRun, Unit: "bytes",
			Used: stored, Allowed: limits.MaxBytesPerRun, Requested: size,
		}
	}
	return nil
}
