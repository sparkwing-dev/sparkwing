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
	"encoding/json"
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

// ConsumerIdentity is what a resident consumer records about itself:
// enough for an operator to find the process, and enough for a newly
// installed CLI to notice it is being served by an older build.
type ConsumerIdentity struct {
	PID     int       `json:"pid"`
	Version string    `json:"version,omitempty"`
	Started time.Time `json:"started"`
}

// ConsumerPID returns the pid recorded by the resident consumer, or
// (0, false) when no consumer holds the lock. The liveness verdict comes
// from the lock; the pid file only names who holds it, and is ignored
// when it disagrees.
func ConsumerPID(home string) (int, bool) {
	id, ok := ConsumerInfo(home)
	if !ok {
		return 0, false
	}
	return id.PID, true
}

// ConsumerInfo returns the resident consumer's recorded identity, or
// (zero, false) when no consumer holds home's lock.
//
// The version is what lets an upgrade take effect. A consumer holds its
// queue for as long as work keeps arriving, so a freshly installed CLI
// that only asked "is one running?" would keep handing runs to the old
// build indefinitely -- the answer stays yes, and the newer binary never
// gets to serve.
func ConsumerInfo(home string) (ConsumerIdentity, bool) {
	running, err := ConsumerRunning(home)
	if err != nil || !running {
		return ConsumerIdentity{}, false
	}
	l, err := ConsumerLayoutFor(home)
	if err != nil {
		return ConsumerIdentity{}, false
	}
	b, err := os.ReadFile(l.PID)
	if err != nil {
		return ConsumerIdentity{}, false
	}
	var id ConsumerIdentity
	if err := json.Unmarshal(b, &id); err != nil {
		// A bare pid is what older consumers wrote. Reading it keeps a
		// mixed-version home operable rather than reporting no consumer
		// while one plainly holds the lock.
		pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if perr != nil || pid <= 0 {
			return ConsumerIdentity{}, false
		}
		return ConsumerIdentity{PID: pid}, true
	}
	if id.PID <= 0 {
		return ConsumerIdentity{}, false
	}
	return id, true
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
	// Version is the build serving this home, recorded alongside the pid
	// so a newer CLI can tell it is being served by an older consumer and
	// rotate it out instead of handing runs to a stale binary.
	Version string
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

	if b, merr := json.Marshal(ConsumerIdentity{
		PID: os.Getpid(), Version: opts.Version, Started: time.Now(),
	}); merr == nil {
		_ = os.WriteFile(layout.PID, append(b, '\n'), 0o644)
	}
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

	inFlight := newInFlightSet()

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
			requeueExpiredClaims(ctx, st, inFlight, logger)
		}

		trig, err := st.ClaimNextTrigger(ctx, rt.lease)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				wedge.success()
				if inFlight.len() == 0 && rt.shouldExit(ctx, st, logger, lastWork) {
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
			if inFlight.len() == 0 && rt.shouldExit(ctx, st, logger, lastWork) {
				return nil
			}
			continue
		}

		lastWork = time.Now()
		// Recorded before the goroutine starts, so the sweeper can never
		// observe this trigger as unowned in the gap between claiming it
		// and beginning to execute it.
		inFlight.add(trig.ID)
		wg.Add(1)
		go func(t *store.Trigger) {
			defer wg.Done()
			defer inFlight.remove(t.ID)
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

// runClaimedTrigger executes one claimed trigger to a terminal outcome,
// holding its lease open for the duration.
//
// The lease heartbeat is what separates "this dispatch is slow" from
// "the consumer died mid-dispatch". Without it every run longer than the
// lease would be swept back onto the queue and executed a second time;
// with it, only a dispatch whose process stopped renewing is recovered.
//
// Terminal bookkeeping runs on a context detached from cancellation.
// The dispatch context is cancelled by `runs consumer stop` and by the
// consumer shutting down, and those are exactly the moments the run's
// outcome most needs recording: writing it through the context that was
// just cancelled leaves the run pending and its trigger claimed, with
// nothing to fix it until a lease lapses minutes later.
func runClaimedTrigger(
	ctx context.Context, st *store.Store, trig *store.Trigger,
	cache *localCompileCache, logger *slog.Logger, lease time.Duration,
) {
	book := context.WithoutCancel(ctx)
	if cancelClaimedTriggerIfRequested(book, st, trig, lease, logger) {
		return
	}

	dispatchCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go heartbeatClaimedTrigger(dispatchCtx, st, trig.ID, trig.ClaimSeq, lease, logger)

	err := dispatchLocalTrigger(dispatchCtx, st, trig, "", "", cache, logger)
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		// The consumer is stopping, so this dispatch was interrupted
		// rather than found faulty. Hand the trigger back to the queue
		// under our own generation instead of failing a run that never
		// got a verdict; the next consumer re-executes it.
		logger.Info("local trigger dispatch interrupted by shutdown; returning it to the queue",
			"trigger_id", trig.ID, "pipeline", trig.Pipeline)
		if _, rerr := st.ReleaseClaimAtGeneration(book, trig.ID, trig.ClaimSeq); rerr != nil {
			logger.Warn("could not return the interrupted trigger to the queue",
				"trigger_id", trig.ID, "err", rerr)
		}
		return
	}
	logger.Error("local trigger dispatch failed",
		"trigger_id", trig.ID, "pipeline", trig.Pipeline, "err", err)

	// The generation is checked BEFORE anything is written, not just
	// before the outcome. CreateRun upserts a pending row, so a
	// superseded dispatch that wrote first would stamp `failed` over the
	// run the current claim is about to start -- and then discover it was
	// superseded, too late to take it back.
	if current, gerr := st.TriggerClaimGeneration(book, trig.ID); gerr == nil && current != trig.ClaimSeq {
		logger.Warn("discarding a superseded dispatch's failure; the current claim owns this run",
			"trigger_id", trig.ID, "claimed_generation", trig.ClaimSeq, "current_generation", current)
		return
	}
	_ = st.CreateRun(book, store.Run{
		ID:        trig.ID,
		Pipeline:  trig.Pipeline,
		Status:    "failed",
		StartedAt: time.Now(),
	})
	if ok, ferr := st.FinishRunAtGeneration(book, trig.ID, trig.ClaimSeq,
		"failed", "local dispatch: "+err.Error()); ferr != nil {
		logger.Warn("record dispatch failure", "trigger_id", trig.ID, "err", ferr)
	} else if !ok {
		logger.Warn("dispatch failure not recorded; the claim was superseded",
			"trigger_id", trig.ID)
		return
	}
	if _, ferr := st.FinishTriggerAtGeneration(book, trig.ID, trig.ClaimSeq); ferr != nil {
		logger.Warn("finish superseded trigger", "trigger_id", trig.ID, "err", ferr)
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

// heartbeatRetryBudget bounds how long the heartbeat keeps trying after
// a transient store failure before it gives up on defending a claim. It
// is a fraction of the lease, so the retries all land inside the window
// they are protecting.
const heartbeatRetryFraction = 3

// heartbeatClaimedTrigger renews trig's lease until ctx ends. A trigger
// that has already been finished by its child reports ErrNotFound, which
// ends the heartbeat quietly -- the dispatch is over.
//
// Any other error is retried with backoff rather than treated as fatal.
// A single "database is locked" from a concurrent dashboard or CLI write
// is an ordinary event on a busy laptop, and giving up on it would leave
// the claim undefended for the rest of a long run -- which is precisely
// the state that lets a sweeper requeue a live dispatch. The heartbeat
// stops only when the claim provably no longer exists, when the
// generation has moved on, or when the dispatch itself ends.
func heartbeatClaimedTrigger(
	ctx context.Context, st *store.Store, id string, seq int64,
	lease time.Duration, logger *slog.Logger,
) {
	interval := lease / heartbeatRetryFraction
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
		if !heartbeatOnce(ctx, st, id, seq, lease, interval, logger) {
			return
		}
	}
}

// heartbeatOnce renews one lease, retrying transient failures inside the
// tick it was given. It reports false when the heartbeat should stop for
// good.
func heartbeatOnce(
	ctx context.Context, st *store.Store, id string, seq int64,
	lease, budget time.Duration, logger *slog.Logger,
) bool {
	deadline := time.Now().Add(budget)
	backoff := 50 * time.Millisecond
	for {
		_, err := st.HeartbeatTrigger(ctx, id, lease)
		if err == nil {
			// A claim taken by someone else is no longer this dispatch's
			// to defend; renewing it would prolong a lease the current
			// holder owns.
			if current, gerr := st.TriggerClaimGeneration(ctx, id); gerr == nil && current != seq {
				logger.Warn("stopping heartbeat; the claim was superseded",
					"trigger_id", id, "claimed_generation", seq, "current_generation", current)
				return false
			}
			return true
		}
		if errors.Is(err, store.ErrNotFound) {
			return false
		}
		if ctx.Err() != nil {
			return false
		}
		if !time.Now().Before(deadline) {
			logger.Warn("local trigger heartbeat failing; the claim may lapse",
				"trigger_id", id, "err", err)
			// Keep the heartbeat alive anyway: the next tick may succeed,
			// and a live dispatch is better defended by a retrying
			// heartbeat than by none at all.
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

// requeueExpiredClaims sweeps claims whose lease lapsed, which is how a
// run acknowledged before its consumer was killed stops sitting claimed
// forever.
//
// It requeues only a run that never started. That restraint is the whole
// point: the lease deadline is an absolute wall-clock instant while the
// heartbeat defending it runs on the monotonic clock, so a laptop
// suspend or an NTP step can lapse the lease of a dispatch that is very
// much alive. Requeueing on that signal alone re-executes a running run
// alongside itself. A run whose row says `running` therefore belongs to
// the orphan reaper, which judges liveness by heartbeat rather than by
// a lease, and a run whose row is already terminal is closed out instead
// -- recovery that re-runs finished work is worse than no recovery.
//
// Terminal rows are reconciled BEFORE anything is requeued, so a crash
// between the two steps cannot leave a finished run sitting pending.
//
// inFlight names the triggers this consumer is executing right now. The
// store cannot know them -- from its side a live local dispatch and a
// dead one look identical -- and without the check a consumer would
// happily sweep its own running work back onto its own queue.
func requeueExpiredClaims(ctx context.Context, st *store.Store, inFlight *inFlightSet, logger *slog.Logger) {
	lapsed, err := st.ListExpiredClaims(ctx)
	if err != nil {
		logger.Warn("local trigger consumer: expired-claim sweep failed", "err", err)
		return
	}
	for _, id := range lapsed {
		if inFlight.has(id) {
			continue
		}
		run, gerr := st.GetRun(ctx, id)
		switch {
		case gerr == nil && run != nil && isTerminalRunStatus(run.Status):
			// Reconciled first: closing the trigger before touching the
			// queue means a crash here cannot strand a finished run.
			if _, ferr := st.FinishTriggerAtGeneration(ctx, id, claimGenerationOf(ctx, st, id)); ferr != nil {
				logger.Warn("close out stale claim", "trigger_id", id, "err", ferr)
				continue
			}
			logger.Warn("closed stale claim whose run already ended",
				"trigger_id", id, "status", run.Status)
		case gerr == nil && run != nil && run.Status != "pending":
			// Running (or any non-terminal, non-pending state): the work
			// started, so liveness is the orphan reaper's judgment to
			// make, not a wall-clock lease's.
			logger.Debug("leaving a started run to the orphan reaper",
				"trigger_id", id, "status", run.Status)
		default:
			requeued, rerr := st.RequeueUnstartedClaim(ctx, id)
			if rerr != nil {
				logger.Warn("requeue stale claim", "trigger_id", id, "err", rerr)
				continue
			}
			if requeued {
				logger.Warn("requeued a claim whose run never started", "trigger_id", id)
			}
		}
	}
}

// claimGenerationOf reads a trigger's current claim generation, or 0
// when it cannot be read. The sweeper uses it to close out a claim it is
// looking at right now, so the read and the write describe the same
// generation.
func claimGenerationOf(ctx context.Context, st *store.Store, id string) int64 {
	seq, err := st.TriggerClaimGeneration(ctx, id)
	if err != nil {
		return 0
	}
	return seq
}

// inFlightSet is the consumer's record of the triggers it is executing.
type inFlightSet struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func newInFlightSet() *inFlightSet {
	return &inFlightSet{ids: map[string]struct{}{}}
}

func (s *inFlightSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = struct{}{}
}

func (s *inFlightSet) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

func (s *inFlightSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok
}

func (s *inFlightSet) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
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
	go serveConsumerContending(ctx, home, st, logger)
	return nil
}

// consumerElectionRetryInterval is how often a consumer that lost the
// election looks again. Short enough that a dashboard picks up a queue
// promptly after the resident consumer idles out, long enough to be a
// negligible poll on a home someone else is serving.
const consumerElectionRetryInterval = 2 * time.Second

// serveConsumerContending serves home's queue whenever it can, retrying
// the election for as long as ctx lives.
//
// Standing down on a lost election is right; standing down *forever* is
// not, and that distinction is the whole reason this loop exists.
// The resident consumer idles out after a few minutes, and a dashboard
// that attempted the election exactly once would then leave the home
// with no consumer at all -- every trigger its own UI, the retry path,
// or a webhook creates sitting pending indefinitely, which is worse than
// the behavior before a standalone consumer existed.
//
// The retry only ever takes a free lock: flockTry fails while a holder
// exists, so contending costs nothing and cannot disturb the consumer
// currently serving.
func serveConsumerContending(ctx context.Context, home string, st *store.Store, logger *slog.Logger) {
	announcedStandDown := false
	for {
		err := ServeConsumer(ctx, ConsumerOptions{
			Home:              home,
			Store:             st,
			Logger:            logger,
			IdleTimeout:       -1,
			NoStaleClaimSweep: true,
		})
		switch {
		case err == nil, errors.Is(err, context.Canceled):
			if ctx.Err() != nil {
				return
			}
		case errors.Is(err, ErrConsumerElectionLost):
			if !announcedStandDown {
				logger.Info("dashboard trigger consumer standing down; " +
					"a resident consumer owns this home's queue. Retrying while it does.")
				announcedStandDown = true
			}
		default:
			logger.Error("dashboard trigger consumer stopped; will retry", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(consumerElectionRetryInterval):
		}
		// Re-arm the notice once the home is free, so the next stand-down
		// is reported rather than swallowed as a repeat. Without this the
		// log would say "standing down" once and never again, even across
		// a genuinely new consumer taking over hours later.
		if held, herr := ConsumerRunning(home); herr == nil && !held {
			announcedStandDown = false
		}
	}
}
