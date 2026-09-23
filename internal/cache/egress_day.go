package cache

import (
	"context"
	"log"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
)

// The cache's egress totals live in memory, so without a controller every
// restart reopens the daily cap and restarts the month's count. With one,
// the day's and the month's totals are kept in the controller's database,
// written at most once a minute and at shutdown, and read back at start.
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

// restoreEgressDay loads today's and this month's stored totals into the
// meter, so a restart resumes the day's cap and the month's count.
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
