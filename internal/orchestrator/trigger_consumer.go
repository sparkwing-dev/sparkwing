// The resident local trigger consumer: the process that makes an
// acknowledged `sparkwing runs submit` a promise rather than a hope.
//
// Submission persists a trigger and a pending run and returns. Something
// has to be alive to claim that trigger, compile the repo, and run it,
// and it cannot be the submitting terminal -- the whole point is that
// the terminal goes away. This file is that something: one process per
// sparkwing home, elected by an exclusive file lock, which claims
// pending triggers until a quiet window passes and then exits so an idle
// laptop runs nothing.
//
// It is deliberately the same claim/dispatch loop the dashboard has
// always hosted (consumeLocalTriggers), not a second implementation. The
// lock is what keeps the two from both consuming: whichever takes it
// consumes, and the other stands down.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// consumerLockFile is the election lock. Its presence means nothing;
	// only a held flock on it does.
	consumerLockFile = "trigger-consumer.lock"
	// consumerPIDFile records the elected consumer's pid for operators
	// and for `runs consumer stop`. It is advisory: the lock, not this
	// file, decides who is running.
	consumerPIDFile = "trigger-consumer.pid"
	// consumerLogFile receives the detached consumer's stdout+stderr.
	consumerLogFile = "trigger-consumer.log"
)

// DefaultConsumerIdleTimeout is how long the consumer sits with no
// pending and no in-flight work before exiting. Long enough that a
// person submitting a few runs in a row keeps reusing one process
// (spawning costs a compile-cache-cold process), short enough that a
// laptop left alone after a submit does not keep one resident all day.
const DefaultConsumerIdleTimeout = 5 * time.Minute

// consumerPollInterval matches the dashboard-hosted loop's cadence.
const consumerPollInterval = 500 * time.Millisecond

// consumerMaintenanceInterval is how often the consumer sweeps expired
// trigger leases back onto the queue. It must be well under the claim
// lease so a crashed dispatch is recovered promptly once its lease
// lapses, and well over the poll interval so the sweep is not a hot
// query on an idle home.
const consumerMaintenanceInterval = 15 * time.Second

// ConsumerLayout names the files one home's resident consumer owns.
type ConsumerLayout struct {
	Home string
	Lock string
	PID  string
	Log  string
}

// ConsumerLayoutFor resolves home's consumer file layout. An empty home
// resolves the canonical one ($SPARKWING_HOME or ~/.sparkwing) through
// DefaultPaths, so callers never read the environment variable directly
// and the test sandbox redirect underneath still applies.
func ConsumerLayoutFor(home string) (ConsumerLayout, error) {
	root := home
	if root == "" {
		p, err := DefaultPaths()
		if err != nil {
			return ConsumerLayout{}, fmt.Errorf("resolve sparkwing home: %w", err)
		}
		root = p.Root
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return ConsumerLayout{}, fmt.Errorf("resolve %s: %w", root, err)
	}
	return ConsumerLayout{
		Home: abs,
		Lock: filepath.Join(abs, consumerLockFile),
		PID:  filepath.Join(abs, consumerPIDFile),
		Log:  filepath.Join(abs, consumerLogFile),
	}, nil
}

// ConsumerRunning reports whether some process holds home's consumer
// lock. It answers by trying to take the lock and immediately dropping
// it, so a consumer that was SIGKILLed reads as not running without any
// stale-file cleanup: the kernel already released its lock.
//
// A missing lock file means no consumer has ever run for this home and
// is not an error.
func ConsumerRunning(home string) (bool, error) {
	l, err := ConsumerLayoutFor(home)
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(l.Lock, os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open %s: %w", l.Lock, err)
	}
	defer func() { _ = f.Close() }()
	ok, err := flockTry(f)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", l.Lock, err)
	}
	if !ok {
		return true, nil
	}
	_ = flockUnlock(f)
	return false, nil
}

// ConsumerPID returns the pid recorded by the resident consumer, or
// (0, false) when no consumer holds the lock. The liveness verdict comes
// from the lock; the pid file only names who holds it, and is ignored
// when it disagrees.
func ConsumerPID(home string) (int, bool) {
	running, err := ConsumerRunning(home)
	if err != nil || !running {
		return 0, false
	}
	l, err := ConsumerLayoutFor(home)
	if err != nil {
		return 0, false
	}
	b, err := os.ReadFile(l.PID)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// ConsumerOptions configures one resident consumer.
type ConsumerOptions struct {
	// Home is the sparkwing state directory to serve. Empty resolves the
	// canonical one.
	Home string
	// IdleTimeout is the quiet window after which the consumer exits.
	// Zero uses DefaultConsumerIdleTimeout; negative disables idle exit,
	// which is what the dashboard-hosted consumer wants -- it lives as
	// long as the dashboard does.
	IdleTimeout time.Duration
	// ClaimLease is the lease stamped on each claimed trigger, renewed by
	// a heartbeat for as long as the dispatch runs. Zero uses
	// store.DefaultLeaseDuration. Tests shorten it so a killed dispatch's
	// recovery is observable in seconds instead of minutes.
	ClaimLease time.Duration
	// Store, when non-nil, is used instead of opening home's SQLite. The
	// dashboard passes the store it already has open; the standalone
	// process leaves it nil and owns the handle.
	Store *store.Store
	// Logger receives the consumer's operational log. Nil uses the
	// default logger.
	Logger *slog.Logger
	// Ready, when non-nil, is closed once the consumer has won the
	// election and is polling. Tests wait on it instead of sleeping.
	Ready chan<- struct{}
	// NoStaleClaimSweep suppresses the expired-lease sweep. The
	// dashboard sets it because the controller it hosts already runs a
	// reaper over the same table; two sweeps racing in one process would
	// have the controller mark a lapsed run failed while this loop puts
	// it back on the queue, and the outcome would depend on which
	// ticker fired first.
	NoStaleClaimSweep bool
}

// ErrConsumerElectionLost reports that another process already holds
// home's consumer lock. It is not a failure: the work is owned, which is
// all the caller wanted. A spawned consumer that loses exits zero, the
// same clean-loss convention the admission daemon's spawn relies on.
var ErrConsumerElectionLost = errors.New("orchestrator: another trigger consumer already serves this home")

// ServeConsumer runs the resident trigger consumer in the foreground
// until ctx is cancelled, the idle window elapses, or the store wedges.
// It returns ErrConsumerElectionLost without doing anything when another
// consumer already holds the lock.
//
// The lock is held for the whole serving lifetime and released on
// return, so `ConsumerRunning` is true exactly while this function is
// consuming -- including across a crash, where the kernel does the
// releasing.
func ServeConsumer(ctx context.Context, opts ConsumerOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	layout, err := ConsumerLayoutFor(opts.Home)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(layout.Home, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", layout.Home, err)
	}

	lockF, err := os.OpenFile(layout.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", layout.Lock, err)
	}
	defer func() { _ = lockF.Close() }()
	won, err := flockTry(lockF)
	if err != nil {
		return fmt.Errorf("lock %s: %w", layout.Lock, err)
	}
	if !won {
		return ErrConsumerElectionLost
	}
	defer func() { _ = flockUnlock(lockF) }()

	_ = os.WriteFile(layout.PID, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	defer func() { _ = os.Remove(layout.PID) }()

	st := opts.Store
	if st == nil {
		p := PathsAt(layout.Home)
		opened, oerr := store.Open(p.StateDB())
		if oerr != nil {
			return fmt.Errorf("open %s: %w", p.StateDB(), oerr)
		}
		defer func() { _ = opened.Close() }()
		st = opened
	}

	wedge, err := newStoreWedgeGuardFromEnv()
	if err != nil {
		return fmt.Errorf("trigger consumer: %w", err)
	}

	logger.Info("trigger consumer serving",
		"home", layout.Home, "pid", os.Getpid(), "idle_timeout", opts.idleTimeout())
	if opts.Ready != nil {
		close(opts.Ready)
	}
	return consumeLocalTriggers(ctx, st, logger, wedge, consumerRuntime{
		idle:     opts.idleTimeout(),
		lease:    opts.claimLease(),
		unlock:   func() { _ = flockUnlock(lockF) },
		relock:   func() bool { ok, lerr := flockTry(lockF); return lerr == nil && ok },
		maintain: !opts.NoStaleClaimSweep,
	})
}

func (o ConsumerOptions) idleTimeout() time.Duration {
	if o.IdleTimeout == 0 {
		return DefaultConsumerIdleTimeout
	}
	return o.IdleTimeout
}

func (o ConsumerOptions) claimLease() time.Duration {
	if o.ClaimLease <= 0 {
		return store.DefaultLeaseDuration
	}
	return o.ClaimLease
}

// consumerRuntime is the policy the claim loop runs under. The
// dashboard-hosted loop and the standalone process differ only here:
// the dashboard never idles out and holds no lock to hand back.
type consumerRuntime struct {
	// idle is the quiet window before exit; <= 0 never exits.
	idle time.Duration
	// lease is stamped on each claim and renewed while dispatching.
	lease time.Duration
	// unlock / relock hand the election lock back before an idle exit and
	// take it again if work appeared in the handover window. Nil on a
	// loop that holds no lock.
	unlock func()
	relock func() bool
	// maintain enables the expired-lease sweep. Only the process that
	// owns the queue should run it.
	maintain bool
}

// consumeLocalTriggers is the claim/dispatch loop shared by the
// dashboard-hosted consumer and the standalone one.
func consumeLocalTriggers(
	ctx context.Context, st *store.Store, logger *slog.Logger,
	wedge *storeWedgeGuard, rt consumerRuntime,
) error {
	cache := &localCompileCache{}
	defer cache.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	var inFlight sync.WaitGroup
	busy := &busyCounter{}

	ticker := time.NewTicker(consumerPollInterval)
	defer ticker.Stop()
	lastWork := time.Now()
	lastMaintenance := time.Now()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if rt.maintain && time.Since(lastMaintenance) >= consumerMaintenanceInterval {
			lastMaintenance = time.Now()
			requeueExpiredClaims(ctx, st, logger)
		}

		trig, err := st.ClaimNextTrigger(ctx, rt.lease)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				wedge.success()
				if busy.idle() && rt.shouldExit(ctx, st, logger, lastWork) {
					return nil
				}
				continue
			}
			if terminal := wedge.fail("local trigger consumer: claim trigger", err); terminal != nil {
				logger.Error("local trigger consumer stopping; store wedged", "err", terminal)
				return terminal
			}
			logger.Warn("local trigger consumer: claim failed", "err", err)
			continue
		}
		wedge.success()
		if trig == nil {
			if busy.idle() && rt.shouldExit(ctx, st, logger, lastWork) {
				return nil
			}
			continue
		}

		lastWork = time.Now()
		busy.enter()
		wg.Add(1)
		inFlight.Add(1)
		go func(t *store.Trigger) {
			defer wg.Done()
			defer inFlight.Done()
			defer busy.leave()
			runClaimedTrigger(ctx, st, t, cache, logger, rt.lease)
		}(trig)
	}
}

// shouldExit decides whether an idle window has really ended the
// consumer's job, and hands the election lock back when it has.
//
// The re-check after unlocking is what makes an acknowledged submission
// safe. A submitter persists its trigger and then asks whether a
// consumer is running; this consumer counts pending work and then
// releases its lock. Without the second count, the interleaving where
// the submitter's insert lands between this consumer's count and its
// release leaves a persisted trigger with nobody to run it: the
// submitter saw a held lock and did not spawn, and this consumer saw an
// empty queue and left. Counting once more after the lock is free closes
// it -- in that interleaving either we now see the row, or the submitter
// now sees a free lock. Both orderings end with someone owning the work.
func (rt consumerRuntime) shouldExit(ctx context.Context, st *store.Store, logger *slog.Logger, lastWork time.Time) bool {
	if rt.idle <= 0 || time.Since(lastWork) < rt.idle {
		return false
	}
	if n, err := st.CountPendingTriggers(ctx); err != nil || n > 0 {
		return false
	}
	if rt.unlock == nil {
		return true
	}
	rt.unlock()
	if n, err := st.CountPendingTriggers(ctx); err == nil && n == 0 {
		logger.Info("trigger consumer idle; exiting", "idle_for", rt.idle)
		return true
	}
	// Work arrived in the handover window. Take the lock back if it is
	// still free; if a fresh consumer already has it, that consumer owns
	// the work and this one is done.
	if rt.relock != nil && rt.relock() {
		logger.Info("trigger consumer resuming; work arrived during idle handover")
		return false
	}
	logger.Info("trigger consumer standing down; another consumer took the queue")
	return true
}

// busyCounter tracks in-flight dispatches so an idle window is judged on
// real quiet, not merely on an empty pending queue while a long run is
// still executing.
type busyCounter struct {
	mu sync.Mutex
	n  int
}

func (b *busyCounter) enter() { b.mu.Lock(); b.n++; b.mu.Unlock() }
func (b *busyCounter) leave() { b.mu.Lock(); b.n--; b.mu.Unlock() }
func (b *busyCounter) idle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n == 0
}

// runClaimedTrigger executes one claimed trigger to a terminal outcome,
// holding its lease open for the duration.
//
// The lease heartbeat is what separates "this dispatch is slow" from
// "the consumer died mid-dispatch". Without it every run longer than the
// lease would be swept back onto the queue and executed a second time;
// with it, only a dispatch whose process stopped renewing is recovered.
func runClaimedTrigger(
	ctx context.Context, st *store.Store, trig *store.Trigger,
	cache *localCompileCache, logger *slog.Logger, lease time.Duration,
) {
	if cancelClaimedTriggerIfRequested(ctx, st, trig, lease, logger) {
		return
	}

	dispatchCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go heartbeatClaimedTrigger(dispatchCtx, st, trig.ID, lease, logger)

	if err := dispatchLocalTrigger(dispatchCtx, st, trig, "", "", cache, logger); err != nil {
		logger.Error("local trigger dispatch failed",
			"trigger_id", trig.ID, "pipeline", trig.Pipeline, "err", err)
		_ = st.CreateRun(ctx, store.Run{
			ID:        trig.ID,
			Pipeline:  trig.Pipeline,
			Status:    "failed",
			StartedAt: time.Now(),
		})
		_ = st.FinishRun(ctx, trig.ID, "failed", "local dispatch: "+err.Error())
		_ = st.FinishTrigger(ctx, trig.ID)
	}
}

// cancelClaimedTriggerIfRequested closes the window between a cancel
// request and a claim: an operator can cancel a submitted run in the
// instant the consumer is picking it up, and the claim would otherwise
// win and start the work anyway. The claim heartbeat reports the pending
// cancel flag, so one call before dispatch turns that race into a
// cancelled run instead of a run that ignored its cancellation.
func cancelClaimedTriggerIfRequested(
	ctx context.Context, st *store.Store, trig *store.Trigger,
	lease time.Duration, logger *slog.Logger,
) bool {
	cancelled, err := st.HeartbeatTrigger(ctx, trig.ID, lease)
	if err != nil || !cancelled {
		return false
	}
	logger.Info("local trigger cancelled before dispatch", "trigger_id", trig.ID)
	_ = st.CreateRun(ctx, store.Run{
		ID:        trig.ID,
		Pipeline:  trig.Pipeline,
		Status:    "pending",
		StartedAt: time.Now(),
	})
	_ = st.FinishRun(ctx, trig.ID, "cancelled", "cancelled before dispatch")
	_ = st.FinishTrigger(ctx, trig.ID)
	return true
}

// heartbeatClaimedTrigger renews trig's lease until ctx ends. A trigger
// that has already been finished by its child reports ErrNotFound, which
// ends the heartbeat quietly -- the dispatch is over.
func heartbeatClaimedTrigger(ctx context.Context, st *store.Store, id string, lease time.Duration, logger *slog.Logger) {
	interval := lease / 3
	if interval < 200*time.Millisecond {
		interval = 200 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := st.HeartbeatTrigger(ctx, id, lease); err != nil {
			if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
				logger.Warn("local trigger heartbeat failed", "trigger_id", id, "err", err)
			}
			return
		}
	}
}

// requeueExpiredClaims sweeps claims whose lease lapsed back onto the
// pending queue, which is how a run acknowledged before its consumer was
// killed becomes runnable again rather than sitting claimed forever.
//
// A trigger whose run already reached a terminal status is finished
// instead of requeued: its work is done, and re-executing it would turn
// recovery into duplication.
func requeueExpiredClaims(ctx context.Context, st *store.Store, logger *slog.Logger) {
	ids, err := store.Maintenance.ReapExpiredTriggers(st, ctx)
	if err != nil {
		logger.Warn("local trigger consumer: expired-claim sweep failed", "err", err)
		return
	}
	for _, id := range ids {
		run, gerr := st.GetRun(ctx, id)
		if gerr == nil && run != nil && isTerminalRunStatus(run.Status) {
			_ = st.FinishTrigger(ctx, id)
			logger.Warn("finished stale claim whose run already ended",
				"trigger_id", id, "status", run.Status)
			continue
		}
		logger.Warn("requeued stale claim for recovery", "trigger_id", id)
	}
}

// isTerminalRunStatus reports whether a run status admits no further
// execution.
func isTerminalRunStatus(status string) bool {
	switch status {
	case "success", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// RunLocalTriggerConsumer starts a background trigger consumer for
// home, against an already-open store, for a caller whose own lifetime
// bounds it -- the dashboard. It returns as soon as the loop is started;
// the loop stops when ctx is cancelled.
//
// It competes for the same election lock as the standalone consumer and
// stands down when it loses, so a home with both a dashboard and a
// resident consumer never dispatches a trigger twice. Standing down is
// not an error: the queue is being served either way, and the dashboard
// has plenty else to do.
//
// An unparseable [StoreWedgeBudgetEnvVar] is a startup error so the
// misconfiguration fails the caller instead of silently leaving queued
// triggers unconsumed.
func RunLocalTriggerConsumer(ctx context.Context, home string, st *store.Store, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := newStoreWedgeGuardFromEnv(); err != nil {
		return fmt.Errorf("local trigger consumer: %w", err)
	}
	go func() {
		err := ServeConsumer(ctx, ConsumerOptions{
			Home:              home,
			Store:             st,
			Logger:            logger,
			IdleTimeout:       -1,
			NoStaleClaimSweep: true,
		})
		switch {
		case err == nil, errors.Is(err, context.Canceled):
		case errors.Is(err, ErrConsumerElectionLost):
			logger.Info("dashboard trigger consumer standing down; a resident consumer owns this home's queue")
		default:
			logger.Error("dashboard trigger consumer stopped", "err", err)
		}
	}()
	return nil
}
