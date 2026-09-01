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

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	consumerLockFile = "trigger-consumer.lock"

	consumerPIDFile = "trigger-consumer.pid"

	consumerLogFile = "trigger-consumer.log"
)

const DefaultConsumerIdleTimeout = 5 * time.Minute

const consumerPollInterval = 500 * time.Millisecond

const defaultConsumerMaintenanceInterval = 15 * time.Second

func consumerMaintenanceIntervalForLease(lease time.Duration) time.Duration {
	interval := lease / 12
	if interval > defaultConsumerMaintenanceInterval {
		return defaultConsumerMaintenanceInterval
	}
	return interval
}

type ConsumerLayout struct {
	Home string
	Lock string
	PID  string
	Log  string
}

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

type ConsumerIdentity struct {
	PID     int       `json:"pid"`
	Version string    `json:"version,omitempty"`
	Started time.Time `json:"started"`
}

func ConsumerPID(home string) (int, bool) {
	id, ok := ConsumerInfo(home)
	if !ok {
		return 0, false
	}
	return id.PID, true
}

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

type ConsumerOptions struct {
	Home string

	IdleTimeout time.Duration

	ClaimLease time.Duration

	Store *store.Store

	Logger *slog.Logger

	Version string

	Ready chan<- struct{}

	NoStaleClaimSweep bool
}

var ErrConsumerElectionLost = errors.New("orchestrator: another trigger consumer already serves this home")

func ServeConsumer(ctx context.Context, opts ConsumerOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	layout, err := ConsumerLayoutFor(opts.Home)
	if err != nil {
		return err
	}
	if err := fssecure.EnsureDir(layout.Home); err != nil {
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
		_ = fssecure.WriteFile(layout.PID, append(b, '\n'))
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
	if removed, reconcileErr := ReconcileSubmissionEnvironments(ctx, layout.Home, st, 1000); reconcileErr != nil {
		logger.Warn("reconcile submission environments", "err", reconcileErr)
	} else if removed > 0 {
		logger.Info("removed terminal submission environments", "count", removed)
	}

	logger.Info("trigger consumer serving",
		"home", layout.Home, "pid", os.Getpid(), "idle_timeout", opts.idleTimeout())
	if opts.Ready != nil {
		close(opts.Ready)
	}
	return consumeLocalTriggers(ctx, st, logger, wedge, consumerRuntime{
		home:     layout.Home,
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

type consumerRuntime struct {
	home string

	idle time.Duration

	lease time.Duration

	unlock func()
	relock func() bool

	maintain bool
}

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
	maintenanceInterval := consumerMaintenanceIntervalForLease(rt.lease)

	firstObservation := true
	for {
		if firstObservation {
			firstObservation = false
			select {
			case <-ctx.Done():
				return nil
			default:
			}
		} else {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}

		if rt.maintain && time.Since(lastMaintenance) >= maintenanceInterval {
			lastMaintenance = time.Now()
			requeueExpiredClaims(ctx, st, inFlight, logger)
			if _, err := ReconcileSubmissionEnvironments(ctx, rt.home, st, 100); err != nil {
				logger.Warn("reconcile submission environments", "err", err)
			}
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

		inFlight.add(trig.ID)
		wg.Add(1)
		go func(t *store.Trigger) {
			defer wg.Done()
			defer inFlight.remove(t.ID)
			runClaimedTrigger(ctx, st, t, cache, logger, rt.home, rt.lease)
		}(trig)
	}
}

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

	if rt.relock != nil && rt.relock() {
		logger.Info("trigger consumer resuming; work arrived during idle handover")
		return false
	}
	logger.Info("trigger consumer standing down; another consumer took the queue")
	return true
}

func runClaimedTrigger(
	ctx context.Context, st *store.Store, trig *store.Trigger,
	cache *localCompileCache, logger *slog.Logger, home string, lease time.Duration,
) {
	book := context.WithoutCancel(ctx)
	if cancelClaimedTriggerIfRequested(book, st, trig, home, lease, logger) {
		return
	}

	dispatchCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go heartbeatClaimedTrigger(dispatchCtx, st, trig.ID, trig.ClaimSeq, lease, logger)

	env, envErr := submissionEnvironment(home, trig)
	if envErr != nil {
		finishClaimedTriggerFailure(book, st, trig, logger, envErr)
		if err := DiscardSubmissionEnvironment(home, trig.ID); err != nil {
			logger.Warn("discard submission environment", "trigger_id", trig.ID, "err", err)
		}
		return
	}
	env = submissionExecutionEnvironment(env, home)
	err := dispatchLocalTrigger(dispatchCtx, st, trig, "", "", cache, logger, env)
	if err == nil {
		if err := DiscardSubmissionEnvironment(home, trig.ID); err != nil {
			logger.Warn("discard submission environment", "trigger_id", trig.ID, "err", err)
		}
		return
	}
	if ctx.Err() != nil {

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
	if err := DiscardSubmissionEnvironment(home, trig.ID); err != nil {
		logger.Warn("discard submission environment", "trigger_id", trig.ID, "err", err)
	}
}

func finishClaimedTriggerFailure(ctx context.Context, st *store.Store, trig *store.Trigger, logger *slog.Logger, err error) {
	_ = st.CreateRun(ctx, store.Run{ID: trig.ID, Pipeline: trig.Pipeline, Status: "failed", StartedAt: time.Now()})
	if _, finishErr := st.FinishRunAtGeneration(ctx, trig.ID, trig.ClaimSeq, "failed", "local dispatch: "+err.Error()); finishErr != nil {
		logger.Warn("record dispatch failure", "trigger_id", trig.ID, "err", finishErr)
	}
	if _, finishErr := st.FinishTriggerAtGeneration(ctx, trig.ID, trig.ClaimSeq); finishErr != nil {
		logger.Warn("finish failed trigger", "trigger_id", trig.ID, "err", finishErr)
	}
}

func cancelClaimedTriggerIfRequested(
	ctx context.Context, st *store.Store, trig *store.Trigger,
	home string, lease time.Duration, logger *slog.Logger,
) bool {
	cancelled, err := st.HeartbeatTrigger(ctx, trig.ID, lease)
	if err != nil || !cancelled {
		return false
	}
	logger.Info("local trigger cancelled before dispatch", "trigger_id", trig.ID)
	if err := DiscardSubmissionEnvironment(home, trig.ID); err != nil {
		logger.Warn("discard submission environment", "trigger_id", trig.ID, "err", err)
	}
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

const heartbeatRetryFraction = 3

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

type triggerHeartbeatStore interface {
	HeartbeatTrigger(context.Context, string, time.Duration) (bool, error)
	TriggerClaimGeneration(context.Context, string) (int64, error)
}

func heartbeatOnce(
	ctx context.Context, st triggerHeartbeatStore, id string, seq int64,
	lease, budget time.Duration, logger *slog.Logger,
) bool {
	deadline := time.Now().Add(budget)
	backoff := 50 * time.Millisecond
	for {
		_, err := st.HeartbeatTrigger(ctx, id, lease)
		if err == nil {

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

			if _, ferr := st.FinishTriggerAtGeneration(ctx, id, claimGenerationOf(ctx, st, id)); ferr != nil {
				logger.Warn("close out stale claim", "trigger_id", id, "err", ferr)
				continue
			}
			logger.Warn("closed stale claim whose run already ended",
				"trigger_id", id, "status", run.Status)
		case gerr == nil && run != nil && run.Status != "pending":

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

func claimGenerationOf(ctx context.Context, st *store.Store, id string) int64 {
	seq, err := st.TriggerClaimGeneration(ctx, id)
	if err != nil {
		return 0
	}
	return seq
}

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

func isTerminalRunStatus(status string) bool {
	switch status {
	case "success", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func RunLocalTriggerConsumer(ctx context.Context, home string, st *store.Store, logger *slog.Logger) error {
	_, err := runLocalTriggerConsumerWithRetryInterval(ctx, home, st, logger, consumerElectionRetryInterval)
	return err
}

func runLocalTriggerConsumerWithRetryInterval(
	ctx context.Context,
	home string,
	st *store.Store,
	logger *slog.Logger,
	retryInterval time.Duration,
) (<-chan struct{}, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := newStoreWedgeGuardFromEnv(); err != nil {
		return nil, fmt.Errorf("local trigger consumer: %w", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveConsumerContending(ctx, home, st, logger, retryInterval)
	}()
	return done, nil
}

const consumerElectionRetryInterval = 2 * time.Second

func serveConsumerContending(
	ctx context.Context,
	home string,
	st *store.Store,
	logger *slog.Logger,
	retryInterval time.Duration,
) {
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
		case <-time.After(retryInterval):
		}

		if held, herr := ConsumerRunning(home); herr == nil && !held {
			announcedStandDown = false
		}
	}
}
