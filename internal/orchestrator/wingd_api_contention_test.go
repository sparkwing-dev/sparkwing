package orchestrator

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type sample struct {
	kind string
	d    time.Duration
	err  error
}

type collector struct {
	mu sync.Mutex
	s  []sample
}

func (c *collector) add(kind string, start time.Time, err error) {
	c.mu.Lock()
	c.s = append(c.s, sample{kind: kind, d: time.Since(start), err: err})
	c.mu.Unlock()
}

func (c *collector) report(t *testing.T, kind string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var ds []time.Duration
	errs := 0
	for _, s := range c.s {
		if s.kind != kind {
			continue
		}
		if s.err != nil {
			errs++
		}
		ds = append(ds, s.d)
	}
	if len(ds) == 0 {
		t.Logf("%-12s no samples", kind)
		return
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	at := func(p float64) time.Duration {
		i := int(p * float64(len(ds)-1))
		return ds[i]
	}
	t.Logf("%-12s n=%3d errors=%d p50=%-10s p95=%-10s p99=%-10s max=%s",
		kind, len(ds), errs,
		at(0.50).Round(time.Microsecond), at(0.95).Round(time.Microsecond),
		at(0.99).Round(time.Microsecond), ds[len(ds)-1].Round(time.Microsecond))
}

// readStallCeiling is the read latency that separates a served read from one
// that queued behind the foreign writer. The writer holds the lock for
// foreignHold, so a read routed onto the writing handle lands near that and a
// read on the WAL reader lands in the low milliseconds.
const readStallCeiling = 1500 * time.Millisecond

const foreignHold = 3 * time.Second

// TestWingdAPIReadsDoNotWaitOnAForeignWriter drives a run's worth of
// controller API traffic per simulated run while a process outside the daemon
// holds a write transaction, and holds the read routes and the admission
// path's own store read clear of it.
func TestWingdAPIReadsDoNotWaitOnAForeignWriter(t *testing.T) {
	home := wingdTestHome(t)
	createStore(t, home)
	sock, runs := startAPIDaemon(t, home, nil)

	httpClient := apiHTTPClient(sock)
	c := client.New(apiBaseURL, httpClient)
	conc := NewHTTPConcurrency(apiBaseURL, httpClient, "", 30*time.Second)

	const runCount = 6
	for i := range runCount {
		seedRun(t, c, fmt.Sprintf("r%d", i), "n0")
	}
	ctx := context.Background()
	for i := range runCount {
		key := fmt.Sprintf("memo:m%d", i)
		resp, err := conc.AcquireSlot(ctx, store.AcquireSlotRequest{
			Key: key, RunID: fmt.Sprintf("r%d", i), NodeID: "n0",
			HolderID: fmt.Sprintf("h%d", i), Policy: "memoize", Lease: 30 * time.Second,
		})
		if err != nil || resp.Kind != store.AcquireGranted {
			t.Fatalf("AcquireSlot %s: kind=%q err=%v", key, resp.Kind, err)
		}
	}

	col := &collector{}
	runCtx, stop := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	loop := func(kind string, every time.Duration, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(every)
			defer tick.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-tick.C:
					start := time.Now()
					err := fn(runCtx)
					if runCtx.Err() != nil {
						return
					}
					col.add(kind, start, err)
				}
			}
		}()
	}

	for i := range runCount {
		runID := fmt.Sprintf("r%d", i)
		key := fmt.Sprintf("memo:m%d", i)
		holder := fmt.Sprintf("h%d", i)
		loop("triggerpoll", 500*time.Millisecond, func(ctx context.Context) error {
			_, err := c.ListTriggers(ctx, store.TriggerFilter{Statuses: []string{"pending"}, Limit: 50})
			return err
		})
		loop("nodebeat", 5*time.Second, func(ctx context.Context) error {
			return c.TouchNodeHeartbeat(ctx, runID, "n0")
		})
		loop("slotbeat", 2*time.Second, func(ctx context.Context) error {
			_, _, err := conc.HeartbeatSlot(ctx, key, holder, 30*time.Second)
			return err
		})
	}
	loop("admission", 200*time.Millisecond, func(context.Context) error {
		_, err := runs.IsRunTerminal("r0")
		return err
	})

	time.Sleep(2 * time.Second)
	held := holdForeignWrite(t, home, foreignHold)
	time.Sleep(foreignHold + 2*time.Second)
	stop()
	wg.Wait()
	<-held

	for _, kind := range []string{"triggerpoll", "nodebeat", "slotbeat", "admission"} {
		col.report(t, kind)
	}
	for _, kind := range []string{"triggerpoll", "admission"} {
		if worst := col.worst(kind); worst >= readStallCeiling {
			t.Errorf("%s waited %s on the foreign writer, over the %s a served read may take",
				kind, worst.Round(time.Millisecond), readStallCeiling)
		}
	}
}

func (c *collector) worst(kind string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var worst time.Duration
	for _, s := range c.s {
		if s.kind == kind && s.d > worst {
			worst = s.d
		}
	}
	return worst
}

func holdForeignWrite(t *testing.T, home string, hold time.Duration) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx := context.Background()
		foreign, err := store.Open(PathsAt(home).StateDB())
		if err != nil {
			t.Errorf("foreign open: %v", err)
			return
		}
		defer func() { _ = foreign.Close() }()
		tx, err := foreign.DB().BeginTx(ctx, nil)
		if err != nil {
			t.Errorf("foreign begin: %v", err)
			return
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sparkwing_meta (key, value, updated_at) VALUES ('api-latency-probe', '1', ?)`,
			time.Now().UnixNano()); err != nil {
			_ = tx.Rollback()
			t.Errorf("foreign write: %v", err)
			return
		}
		t.Logf("foreign writer holds the write lock for %s", hold)
		time.Sleep(hold)
		_ = tx.Rollback()
		t.Logf("foreign writer released")
	}()
	return done
}
