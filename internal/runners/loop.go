package runners

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// ClaimController claims through the shared queue and must honor context cancellation.
type ClaimController interface {
	Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
}

// UnitHost dispatches awards to shared hosts and joins their lease keepers before returning.
type UnitHost interface {
	Host(context.Context, controllerwire.ClaimedUnit) error
}

// LoopConfig selects claim kinds, capacity and lifecycle boundaries.
type LoopConfig struct {
	Version       string
	Labels        []string
	RunID         string
	Kinds         []capability.UnitKind
	MaxConcurrent int
	MaxClaims     int
	PollInterval  time.Duration
	IdleExit      time.Duration
	ClaimUntil    time.Time
}

// Loop claims triggers and jobs until stopped and joins held work before returning.
type Loop struct {
	Config          LoopConfig
	Controller      ClaimController
	Host            UnitHost
	CheckVersion    func(context.Context, string) error
	RequestCapacity func() (cores float64, memoryBytes int64)
	OnError         func(error)
	mu              sync.Mutex
	drain           chan struct{}
	running         bool
	draining        bool
}

// Drain may be called before or during Run and is safe to repeat.
func (l *Loop) Drain() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.initDrain()
	if !l.draining {
		close(l.drain)
		l.draining = true
	}
}

func (l *Loop) initDrain() {
	if l.drain == nil {
		l.drain = make(chan struct{})
	}
}

func (l *Loop) start() (<-chan struct{}, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return nil, errors.New("runner loop has already run")
	}
	if l.Controller == nil || l.Host == nil || l.CheckVersion == nil {
		return nil, errors.New("runner requires a controller, unit host and signed version policy")
	}
	if l.Config.Version == "" || l.Config.MaxConcurrent < 1 || l.Config.IdleExit < 0 || l.Config.PollInterval < 0 || l.Config.MaxClaims < 0 {
		return nil, errors.New("runner requires its release version, positive concurrency and nonnegative durations and claim count")
	}
	if l.Config.Kinds == nil {
		l.Config.Kinds = []capability.UnitKind{capability.UnitTrigger}
		if l.Config.RunID == "" {
			l.Config.Kinds = append(l.Config.Kinds, capability.UnitNode)
		}
	}
	if len(l.Config.Kinds) == 0 {
		return nil, errors.New("runner must claim triggers or jobs")
	}
	for i, kind := range l.Config.Kinds {
		if (kind != capability.UnitTrigger && kind != capability.UnitNode) || slices.Contains(l.Config.Kinds[:i], kind) {
			return nil, errors.New("runner claim kinds must be distinct triggers or jobs")
		}
	}
	if l.Config.PollInterval == 0 {
		l.Config.PollInterval = limits.ClaimPoll
	}
	l.running = true
	l.initDrain()
	return l.drain, nil
}

type unitResult struct {
	key capability.LeaseRef
	err error
}

// Run checks signed runner policy before claims and drains held work on denial.
func (l *Loop) Run(ctx context.Context) error {
	policyErr, runErr := l.run(ctx)
	return errors.Join(policyErr, runErr)
}

func (l *Loop) run(ctx context.Context) (policyErr, runErr error) {
	drain, err := l.start()
	if err != nil {
		return nil, err
	}
	claims, cancelClaims := context.WithCancel(ctx)
	defer cancelClaims()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-drain:
			cancelClaims()
		case <-claims.Done():
		}
	}()
	defer func() { cancelClaims(); <-watchDone }()
	policyRefresh := time.NewTicker(limits.VersionIndexRefresh)
	defer policyRefresh.Stop()
	results := make(chan unitResult, l.Config.MaxConcurrent)
	active := make(map[capability.LeaseRef]struct{})
	idleSince := time.Now()
	claimed := 0
	var hostErr error
	finish := func(result unitResult) {
		delete(active, result.key)
		if len(active) == 0 {
			idleSince = time.Now()
		}
		if result.err != nil {
			if l.OnError != nil {
				l.OnError(result.err)
			}
			if hostErr == nil {
				hostErr = result.err
			}
		}
	}
	defer func() {
		for len(active) > 0 {
			finish(<-results)
		}
		if hostErr != nil {
			runErr = errors.Join(runErr, hostErr)
		}
		if cause := context.Cause(ctx); cause != nil && !errors.Is(runErr, cause) {
			runErr = errors.Join(runErr, cause)
		}
	}()
	drainResults := func() {
		for {
			select {
			case result := <-results:
				finish(result)
			default:
				return
			}
		}
	}
	for {
		drainResults()
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		select {
		case <-drain:
			return nil, nil
		default:
		}
		if l.Config.MaxClaims > 0 && claimed >= l.Config.MaxClaims {
			return nil, nil
		}
		if len(active) >= l.Config.MaxConcurrent {
			select {
			case result := <-results:
				finish(result)
				idleSince = time.Now()
			case <-policyRefresh.C:
				if stop, err := l.checkPolicy(claims); stop {
					return err, nil
				}
			case <-drain:
				return nil, nil
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
			continue
		}
		select {
		case <-drain:
			return nil, nil
		default:
		}
		if !l.Config.ClaimUntil.IsZero() && !time.Now().Before(l.Config.ClaimUntil) {
			return nil, nil
		}
		if len(active) == 0 && l.Config.IdleExit > 0 && time.Since(idleSince) >= l.Config.IdleExit {
			return nil, nil
		}
		if stop, err := l.checkPolicy(claims); stop {
			return err, nil
		}
		outcome := l.claimWhileHosting(claims, policyRefresh.C, results, finish, func() time.Duration {
			return l.lifecycleWait(limits.VersionIndexRefresh, idleSince, len(active) == 0)
		})
		if outcome.response.Claim != nil {
			drainResults()
			unit := *outcome.response.Claim
			if err := validateUnit(unit); err != nil {
				return outcome.stopErr, err
			}
			if !slices.Contains(l.Config.Kinds, unit.Unit) || (l.Config.RunID != "" && unit.RunID != l.Config.RunID) {
				return outcome.stopErr, errors.New("controller awarded work outside the runner's requested selection")
			}
			key := capability.LeaseRef{Kind: unit.Unit, ID: unit.UnitID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation}
			if _, exists := active[key]; exists {
				return outcome.stopErr, errors.New("controller awarded a unit already held by this runner")
			}
			active[key] = struct{}{}
			claimed++
			go func() { results <- unitResult{key: key, err: l.Host.Host(ctx, unit)} }()
		}
		if ctx.Err() != nil {
			return outcome.stopErr, context.Cause(ctx)
		}
		if outcome.stopped {
			return outcome.stopErr, nil
		}
		if outcome.err != nil {
			return nil, fmt.Errorf("claim runner work: %w", outcome.err)
		}
		if outcome.response.Claim != nil {
			continue
		}
		timer := time.NewTimer(l.pollWait(idleSince, len(active) == 0))
	pollWait:
		for {
			select {
			case result := <-results:
				finish(result)
				break pollWait
			case <-timer.C:
				break pollWait
			case <-policyRefresh.C:
				if stop, err := l.checkPolicy(claims); stop {
					timer.Stop()
					return err, nil
				}
			case <-drain:
				timer.Stop()
				return nil, nil
			case <-ctx.Done():
				timer.Stop()
				return nil, context.Cause(ctx)
			}
		}
		timer.Stop()
	}
}

func (l *Loop) pollWait(idleSince time.Time, idle bool) time.Duration {
	return l.lifecycleWait(l.Config.PollInterval, idleSince, idle)
}

func (l *Loop) lifecycleWait(wait time.Duration, idleSince time.Time, idle bool) time.Duration {
	if idle && l.Config.IdleExit > 0 {
		wait = min(wait, time.Until(idleSince.Add(l.Config.IdleExit)))
	}
	if !l.Config.ClaimUntil.IsZero() {
		wait = min(wait, time.Until(l.Config.ClaimUntil))
	}
	return max(wait, 0)
}

type claimOutcome struct {
	response controllerwire.ClaimResponse
	err      error
	stopped  bool
	stopErr  error
}

func (l *Loop) claimWhileHosting(ctx context.Context, refresh <-chan time.Time, results <-chan unitResult, finish func(unitResult), wait func() time.Duration) claimOutcome {
	claimCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan claimOutcome, 1)
	request := l.request()
	go func() {
		response, err := l.Controller.Claim(claimCtx, request)
		done <- claimOutcome{response: response, err: err}
	}()
	timer := time.NewTimer(wait())
	defer timer.Stop()
	for {
		var stopErr error
		select {
		case outcome := <-done:
			outcome.stopped = ctx.Err() != nil
			return outcome
		case result := <-results:
			finish(result)
			timer.Reset(wait())
			continue
		case <-refresh:
			stop, err := l.checkPolicy(ctx)
			if !stop {
				continue
			}
			stopErr = err
		case <-timer.C:
			if remaining := wait(); remaining > 0 {
				timer.Reset(remaining)
				continue
			}
		case <-ctx.Done():
		}
		cancel()
		outcome := <-done
		outcome.stopped, outcome.stopErr = true, stopErr
		return outcome
	}
}

func (l *Loop) checkPolicy(ctx context.Context) (bool, error) {
	err := l.CheckVersion(ctx, l.Config.Version)
	if err == nil || policyInterrupted(ctx, err) {
		return err != nil, nil
	}
	return true, fmt.Errorf("runner version policy: %w", err)
}

func policyInterrupted(ctx context.Context, err error) bool {
	if ctx.Err() == nil || err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts := joined.Unwrap()
		for _, part := range parts {
			if !policyInterrupted(ctx, part) {
				return false
			}
		}
		return len(parts) > 0
	}
	if wrapped := errors.Unwrap(err); wrapped != nil {
		return policyInterrupted(ctx, wrapped)
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (l *Loop) request() controllerwire.ClaimRequest {
	req := controllerwire.ClaimRequest{
		RunnerVersion: l.Config.Version,
		Kinds:         slices.Clone(l.Config.Kinds),
		Labels:        slices.Clone(l.Config.Labels), RunID: l.Config.RunID,
	}
	if l.RequestCapacity != nil {
		req.AvailableCores, req.AvailableMemoryBytes = l.RequestCapacity()
	}
	return req
}

func validateUnit(unit controllerwire.ClaimedUnit) error {
	if unit.Unit != capability.UnitTrigger && unit.Unit != capability.UnitNode {
		return errors.New("controller awarded a unit this runner does not execute")
	}
	if unit.UnitID == "" || unit.RunID == "" || unit.Capability == "" {
		return errors.New("controller award is missing unit identity or authority")
	}
	return nil
}
