package cache

import (
	"context"
	"log"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// safety: the meter counts in memory, so the controller keeps its day and month totals
// for a restart to resume rather than reopen the daily cap.
const egressDayFlushEvery = time.Minute

const egressService = "cache"

func flushEgressDay(ctx context.Context) error {
	if counter == nil || egressMeter == nil {
		return nil
	}
	return egressMeter.FlushDay(func(u egress.DayUsage) error {
		_, err := counter.RecordEgress(ctx, counterAuth, storagequota.EgressTotals{
			Service: egressService, Day: u.Day, DayBytes: u.Bytes, Month: u.Month, MonthBytes: u.MonthBytes,
		})
		return err
	})
}

func restoreEgressDay(ctx context.Context) error {
	if counter == nil || egressMeter == nil {
		return nil
	}
	now := time.Now().UTC()
	got, err := counter.RecordEgress(ctx, counterAuth, storagequota.EgressTotals{
		Service: egressService, Day: now.Format("2006-01-02"), Month: now.Format("2006-01"),
	})
	if err != nil {
		return err
	}
	egressMeter.RestoreDay(egress.DayUsage{Day: got.Day, Bytes: got.DayBytes, Month: got.Month, MonthBytes: got.MonthBytes})
	return nil
}

func egressDayLoop(ctx context.Context) {
	t := time.NewTicker(egressDayFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := flushEgressDay(sctx); err != nil {
				log.Printf("warning: save the day's egress total: %v", err)
			}
			cancel()
			return
		case <-t.C:
			if err := flushEgressDay(ctx); err != nil {
				log.Printf("warning: save the day's egress total: %v", err)
			}
		}
	}
}
