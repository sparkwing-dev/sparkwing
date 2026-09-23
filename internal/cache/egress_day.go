package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// The cache's daily egress total lives in memory, so without a bucket every
// restart reopens the daily cap. With a bucket the day's total is one small
// object in the operator's namespace, written at most once a minute and at
// shutdown, and read back at start.
const egressDayFlushEvery = time.Minute

func egressDayRel(day string) string { return "egress/" + day + ".json" }

func flushEgressDay(ctx context.Context) error {
	if blobStore == nil || egressMeter == nil {
		return nil
	}
	return egressMeter.FlushDay(func(u egress.DayUsage) error {
		body, err := json.Marshal(u)
		if err != nil {
			return err
		}
		_, err = blobStore.Put(ctx, "", egressDayRel(u.Day), bytes.NewReader(body), teamblob.PutOptions{
			Size: int64(len(body)), ContentType: "application/json",
		})
		return err
	})
}

// restoreEgressDay loads today's saved total into the meter, so a restart
// resumes the day's cap rather than reopening it.
func restoreEgressDay(ctx context.Context) error {
	if blobStore == nil || egressMeter == nil {
		return nil
	}
	body, err := blobStore.ReadAll(ctx, "", egressDayRel(time.Now().UTC().Format("2006-01-02")))
	if errors.Is(err, teamblob.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var u egress.DayUsage
	if err := json.Unmarshal(body, &u); err != nil {
		return err
	}
	egressMeter.RestoreDay(u)
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
