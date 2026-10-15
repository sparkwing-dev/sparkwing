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

// ClaimController uses the same queue for every runner configuration. The server
// derives identity, repository authorization and placement from its credential.
type ClaimController interface {
	Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
}

// UnitHost dispatches a trigger to RunHost or a node to NodeHost. It owns the
// shared lease keeper and joins it before returning, including failed startup.
// The award and its capability stay in memory and never enter a child environment.
type UnitHost interface {
	Host(context.Context, controllerwire.ClaimedUnit) error
}

// LoopConfig selects work without creating another placement implementation.
// RunID confines a here runner; the controller still enforces its awarded scope.
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

// Loop claims trigger and node units through one controller queue. Drain stops
// new claims while hosts keep their leases; cancellation stops and joins hosts.
// Each Loop runs once. A new configuration or replacement process uses a new Loop.
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
		l.Config.Kinds = []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
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

type unitKey struct {
	kind       capability.UnitKind
	id         string
	holder     string
	generation int64
}

type unitResult struct {
	key unitKey
	err error
}

type hostFailure struct{ error }

func (e *hostFailure) Unwrap() error { return e.error }

// Run checks the signed runner policy before each award request, independently
// of the pipeline's SDK pin. Policy denial drains existing work before returning.
func (l *Loop) Run(ctx context.Context) (runErr error) {
	drain, err := l.start()
	if err != nil {
		return err
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
	active := make(map[unitKey]struct{})
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
			runErr = errors.Join(runErr, &hostFailure{hostErr})
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
			return context.Cause(ctx)
		}
		select {
		case <-drain:
			return nil
		default:
		}
		if l.Config.MaxClaims > 0 && claimed >= l.Config.MaxClaims {
			return nil
		}
		for len(active) >= l.Config.MaxConcurrent {
			select {
			case result := <-results:
				finish(result)
				idleSince = time.Now()
			case <-policyRefresh.C:
				if err := l.CheckVersion(claims, l.Config.Version); err != nil {
					if claims.Err() != nil && ctx.Err() == nil {
						return nil
					}
					return fmt.Errorf("runner version policy: %w", err)
				}
			case <-drain:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		select {
		case <-drain:
			return nil
		default:
		}
		if !l.Config.ClaimUntil.IsZero() && !time.Now().Before(l.Config.ClaimUntil) {
			return nil
		}
		if len(active) == 0 && l.Config.IdleExit > 0 && time.Since(idleSince) >= l.Config.IdleExit {
			return nil
		}
		if err := l.CheckVersion(claims, l.Config.Version); err != nil {
			if claims.Err() != nil && ctx.Err() == nil {
				return nil
			}
			return fmt.Errorf("runner version policy: %w", err)
		}
		response, err := l.Controller.Claim(claims, l.request())
		if err != nil {
			if claims.Err() != nil {
				if ctx.Err() != nil {
					return context.Cause(ctx)
				}
				return nil
			}
			return fmt.Errorf("claim runner work: %w", err)
		}
		if response.Claim != nil {
			drainResults()
			unit := *response.Claim
			if err := validateUnit(unit); err != nil {
				return err
			}
			if !slices.Contains(l.Config.Kinds, unit.Unit) || (l.Config.RunID != "" && unit.RunID != l.Config.RunID) {
				return errors.New("controller awarded work outside the runner's requested selection")
			}
			key := unitKey{kind: unit.Unit, id: unit.UnitID, holder: unit.Binding.Holder, generation: unit.Binding.Generation}
			if _, exists := active[key]; exists {
				return errors.New("controller awarded a unit already held by this runner")
			}
			active[key] = struct{}{}
			claimed++
			go func() { results <- unitResult{key: key, err: l.Host.Host(ctx, unit)} }()
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
				if err := l.CheckVersion(claims, l.Config.Version); err != nil {
					timer.Stop()
					if claims.Err() != nil && ctx.Err() == nil {
						return nil
					}
					return fmt.Errorf("runner version policy: %w", err)
				}
			case <-drain:
				timer.Stop()
				return nil
			case <-ctx.Done():
				timer.Stop()
				return context.Cause(ctx)
			}
		}
		timer.Stop()
	}
}

func (l *Loop) pollWait(idleSince time.Time, idle bool) time.Duration {
	wait := l.Config.PollInterval
	if idle && l.Config.IdleExit > 0 {
		wait = min(wait, time.Until(idleSince.Add(l.Config.IdleExit)))
	}
	if !l.Config.ClaimUntil.IsZero() {
		wait = min(wait, time.Until(l.Config.ClaimUntil))
	}
	return max(wait, 0)
}

func (l *Loop) request() controllerwire.ClaimRequest {
	req := controllerwire.ClaimRequest{RunnerVersion: l.Config.Version,
		Kinds:  slices.Clone(l.Config.Kinds),
		Labels: slices.Clone(l.Config.Labels), RunID: l.Config.RunID}
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
