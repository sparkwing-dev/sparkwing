package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// Event size limits, named so a refusal says which one it hit.
const (
	storageLimitEventBytes       = "event_bytes"
	storageLimitEventBytesPerRun = "event_bytes_per_run"
	storageLimitEventsPerRun     = "events_per_run"
)

// maxEventKindBytes bounds an event's kind. A kind names a category, so it
// is short and drawn from [A-Za-z0-9_.:-].
const maxEventKindBytes = 128

// errInvalidEventKind is returned when an event's kind is empty, longer than
// [maxEventKindBytes], or holds a byte outside [A-Za-z0-9_.:-].
var errInvalidEventKind = errors.New("invalid event kind")

// defaultEventLimits returns the shared default event caps.
func defaultEventLimits() eventLimits {
	return eventLimits{MaxBytesPerEvent: limits.EventBytes, MaxBytesPerRun: limits.RunEventBytes, MaxEventsPerRun: int64(limits.MaxEvents)}
}

// eventLimits caps what a run stores as events. An event's bytes are its
// kind plus its payload. The caps hold whether or not a storage quota tier is
// configured, because an unconfigured controller is exactly the one with
// nothing else between a runner token and the database's disk. A value of
// zero or less lifts that cap.
type eventLimits struct {
	MaxBytesPerEvent int64
	MaxBytesPerRun   int64
	MaxEventsPerRun  int64
}

const (
	metaKeyEventMaxBytes        = "event_max_bytes"
	metaKeyEventMaxBytesPerRun  = "event_max_bytes_per_run"
	metaKeyEventMaxEventsPerRun = "event_max_events_per_run"
)

// validateEventKind reports [errInvalidEventKind] unless kind is 1 to
// [maxEventKindBytes] bytes of [A-Za-z0-9_.:-].
func validateEventKind(kind string) error {
	if kind == "" || len(kind) > maxEventKindBytes {
		return fmt.Errorf("%w: length %d, want 1 to %d bytes", errInvalidEventKind, len(kind), maxEventKindBytes)
	}
	for i := 0; i < len(kind); i++ {
		c := kind[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '_', c == '.', c == ':', c == '-':
		default:
			return fmt.Errorf("%w: byte %q at %d is outside [A-Za-z0-9_.:-]", errInvalidEventKind, c, i)
		}
	}
	return nil
}

func eventBytes(kind string, payload []byte) int64 {
	return int64(len(kind) + len(payload))
}

// eventLimits returns the caps in force, the defaults where the operator
// set none.
func (s *Store) eventLimits(ctx context.Context) (eventLimits, error) {
	return inTxResult(ctx, s, func(tx *storeTx) (eventLimits, error) {
		return eventLimitsTx(ctx, tx)
	})
}

// setEventLimits replaces every cap; zero or less lifts one.
func (s *Store) setEventLimits(ctx context.Context, limits eventLimits) error {
	return s.inTxOnce(ctx, func(tx *storeTx) error {
		if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxBytes, formatCreditSetting(limits.MaxBytesPerEvent)); err != nil {
			return err
		}
		if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxBytesPerRun, formatCreditSetting(limits.MaxBytesPerRun)); err != nil {
			return err
		}
		if err := setCreditSettingTx(ctx, tx, metaKeyEventMaxEventsPerRun, formatCreditSetting(limits.MaxEventsPerRun)); err != nil {
			return err
		}
		return nil
	})
}

func eventLimitsTx(ctx context.Context, tx *storeTx) (eventLimits, error) {
	defaults := defaultEventLimits()
	perEvent, err := creditSettingTx(ctx, tx, metaKeyEventMaxBytes, defaults.MaxBytesPerEvent)
	if err != nil {
		return eventLimits{}, err
	}
	perRun, err := creditSettingTx(ctx, tx, metaKeyEventMaxBytesPerRun, defaults.MaxBytesPerRun)
	if err != nil {
		return eventLimits{}, err
	}
	events, err := creditSettingTx(ctx, tx, metaKeyEventMaxEventsPerRun, defaults.MaxEventsPerRun)
	if err != nil {
		return eventLimits{}, err
	}
	return eventLimits{MaxBytesPerEvent: perEvent, MaxBytesPerRun: perRun, MaxEventsPerRun: events}, nil
}

func refuseEventOverLimitsTx(ctx context.Context, tx *storeTx, team Team, runID string, size int64) error {
	limits, err := eventLimitsTx(ctx, tx)
	if err != nil {
		return err
	}
	if limits.MaxBytesPerEvent > 0 && size > limits.MaxBytesPerEvent {
		return &StorageQuotaError{
			Principal: string(team), Limit: storageLimitEventBytes, Unit: "bytes",
			Allowed: limits.MaxBytesPerEvent, Requested: size,
		}
	}
	var storedBytes, storedEvents int64
	err = tx.QueryRowContext(ctx,
		`SELECT event_bytes, event_count FROM runs WHERE team = ? AND id = ?`, team, runID).Scan(&storedBytes, &storedEvents)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound("run", runID)
	}
	if err != nil {
		return err
	}
	if storedBytes < 0 || storedEvents < 0 || size > math.MaxInt64-storedBytes || storedEvents == math.MaxInt64 {
		return fmt.Errorf("%w: event accounting overflow", ErrInvalidInput)
	}
	if limits.MaxBytesPerRun > 0 && size > limits.MaxBytesPerRun-storedBytes {
		return &StorageQuotaError{
			Principal: string(team), Limit: storageLimitEventBytesPerRun, Unit: "bytes",
			Used: storedBytes, Allowed: limits.MaxBytesPerRun, Requested: size,
		}
	}
	if limits.MaxEventsPerRun > 0 && storedEvents+1 > limits.MaxEventsPerRun {
		return &StorageQuotaError{
			Principal: string(team), Limit: storageLimitEventsPerRun, Unit: "events",
			Used: storedEvents, Allowed: limits.MaxEventsPerRun, Requested: 1,
		}
	}
	return nil
}
