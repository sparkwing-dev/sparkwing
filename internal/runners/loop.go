package runners

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
	RequestCapacity func(context.Context) (capacity, available match.Resources, err error)
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
	refresh := time.NewTicker(limits.VersionIndexRefresh)
	defer refresh.Stop()
	timer := time.NewTimer(0)
	defer timer.Stop()
	results := make(chan unitResult, l.Config.MaxConcurrent)
	active := make(map[capability.LeaseRef]struct{})
	idleSince, pollAt, backoffUntil := time.Now(), time.Time{}, time.Time{}
	claimed := 0
	var hostErr error
	finish := func(result unitResult) {
		pollAt = time.Time{}
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
	accept := func(outcome claimOutcome) error {
		if outcome.response.Claim == nil {
			return nil
		}
		drainResults()
		unit := *outcome.response.Claim
		if err := validateUnit(unit); err != nil {
			return err
		}
		if !slices.Contains(l.Config.Kinds, unit.Unit) || (l.Config.RunID != "" && unit.RunID != l.Config.RunID) {
			return errors.New("controller awarded work outside the runner's requested selection")
		}
		key := capability.LeaseRef{Kind: unit.Unit, ID: unit.UnitID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation}
		if _, exists := active[key]; exists {
			return errors.New("controller awarded a unit already held by this runner")
		}
		active[key] = struct{}{}
		claimed++
		go func() { results <- unitResult{key: key, err: l.Host.Host(ctx, unit)} }()
		return nil
	}
	var pending <-chan claimOutcome
	defer func() {
		cancelClaims()
		if pending != nil {
			runErr = errors.Join(runErr, accept(<-pending))
		}
		for len(active) > 0 {
			finish(<-results)
		}
		runErr = errors.Join(runErr, hostErr, context.Cause(ctx))
	}()
	for {
		drainResults()
		select {
		case <-drain:
			return nil, nil
		default:
		}
		if claims.Err() != nil || (l.Config.MaxClaims > 0 && claimed >= l.Config.MaxClaims) || l.lifecycleWait(limits.VersionIndexRefresh, idleSince, len(active) == 0) == 0 {
			return nil, nil
		}
		nextClaim := pollAt
		if backoffUntil.After(nextClaim) {
			nextClaim = backoffUntil
		}
		if pending == nil && len(active) < l.Config.MaxConcurrent && !nextClaim.After(time.Now()) {
			if stop, err := l.checkPolicy(claims); stop {
				return err, nil
			}
			done := make(chan claimOutcome, 1)
			pending = done
			go func() {
				request, err := l.request(claims)
				var response controllerwire.ClaimResponse
				if err == nil {
					response, err = l.Controller.Claim(claims, request)
				}
				done <- claimOutcome{response: response, err: err}
			}()
		}
		wait := limits.VersionIndexRefresh
		if pending == nil && nextClaim.After(time.Now()) {
			wait = min(wait, time.Until(nextClaim))
		}
		timer.Reset(l.lifecycleWait(wait, idleSince, len(active) == 0))
		select {
		case outcome := <-pending:
			pending = nil
			if err := accept(outcome); err != nil {
				return nil, err
			}
			if claims.Err() != nil {
				return nil, nil
			}
			select {
			case <-drain:
				return nil, nil
			default:
			}
			if delay, ok := apierr.RetryDelay(outcome.err); ok {
				backoffUntil = time.Now().Add(max(delay, l.Config.PollInterval))
				continue
			}
			if outcome.err != nil {
				return nil, fmt.Errorf("claim runner work: %w", outcome.err)
			}
			if outcome.response.Claim == nil {
				pollAt = time.Now().Add(l.Config.PollInterval)
			}
		case result := <-results:
			finish(result)
		case <-refresh.C:
			if stop, err := l.checkPolicy(claims); stop {
				return err, nil
			}
		case <-timer.C:
		case <-claims.Done():
			return nil, nil
		}
	}
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

func (l *Loop) request(ctx context.Context) (controllerwire.ClaimRequest, error) {
	req := controllerwire.ClaimRequest{
		RunnerVersion: l.Config.Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Kinds:  slices.Clone(l.Config.Kinds),
		Labels: slices.Clone(l.Config.Labels), RunID: l.Config.RunID,
	}
	if l.RequestCapacity != nil {
		capacity, available, err := l.RequestCapacity(ctx)
		if err != nil {
			return req, err
		}
		req.Capacity = &capacity
		req.AvailableCores, req.AvailableMemoryBytes = available.Cores, available.MemoryBytes
	}
	return req, nil
}

func validateUnit(unit controllerwire.ClaimedUnit) error {
	if unit.Unit != capability.UnitTrigger && unit.Unit != capability.UnitNode && unit.Unit != capability.UnitPlan && unit.Unit != capability.UnitEval {
		return errors.New("controller awarded a unit this runner does not execute")
	}
	if unit.UnitID == "" || unit.RunID == "" || unit.Capability == "" {
		return errors.New("controller award is missing unit identity or authority")
	}
	return nil
}
