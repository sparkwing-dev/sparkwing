package runners

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"

	controllerclient "github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
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
	// VersionPinned stops self-update toward a newer release: a GitHub Actions
	// job gets its version from .sparkwing/go.mod, and a restarted process
	// cannot exchange the run attempt's one-shot OIDC token again.
	VersionPinned bool
	// FailOnFailedWork makes the loop return an error naming each run or job it
	// executed that failed or was cancelled, so a GitHub Actions job shows red.
	FailOnFailedWork bool
	// Kubernetes reports to the controller that this runner runs in a pod.
	Kubernetes bool
}

// Loop claims triggers and jobs until stopped and joins held work before returning.
type Loop struct {
	Config       LoopConfig
	Controller   ClaimController
	Host         UnitHost
	CheckVersion func(context.Context, string) error
	OnError      func(error)
	// OnClaimsStopped runs once before joining hosts when the claim loop exits.
	OnClaimsStopped func()
	mu              sync.Mutex
	drain           chan struct{}
	running         bool
	draining        bool
}

// Drain stops new claims and makes Run join held hosts without cancelling their
// contexts. It may be called before or during Run and returns immediately.
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
			l.Config.Kinds = append(l.Config.Kinds, capability.UnitPlan, capability.UnitEval, capability.UnitNode)
		}
	}
	if len(l.Config.Kinds) == 0 {
		return nil, errors.New("runner must claim triggers, plans, evaluations or jobs")
	}
	for i, kind := range l.Config.Kinds {
		named := l.Config.RunID != "" && kind != capability.UnitTrigger && kind != capability.UnitNode
		if named || !slices.Contains([]capability.UnitKind{capability.UnitTrigger, capability.UnitPlan, capability.UnitEval, capability.UnitNode}, kind) || slices.Contains(l.Config.Kinds[:i], kind) {
			return nil, errors.New("runner claim kinds must be distinct triggers, plans, evaluations or jobs")
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

// Run claims work and joins held hosts before returning.
// CheckVersion, when supplied, may stop claims and drain hosts on a policy denial.
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
	workSlots := make(chan struct{}, l.Config.MaxConcurrent)
	ownerSlots := make(chan struct{}, l.Config.MaxConcurrent)
	active := make(map[capability.LeaseRef]struct{})
	awaitChanged := make(chan struct{}, 1)
	idleSince, pollAt, backoffUntil := time.Now(), time.Time{}, time.Time{}
	var transportSince time.Time
	var transportErr error
	transportAttempt := 0
	claimed := 0
	var failed failedWork
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
		defer func() {
			if outcome.workSlot != nil {
				outcome.workSlot()
			}
			if outcome.ownerSlot != nil {
				outcome.ownerSlot()
			}
		}()
		if outcome.response.Claim == nil {
			return nil
		}
		drainResults()
		unit := *outcome.response.Claim
		if err := validateUnit(unit); err != nil {
			return err
		}
		if !slices.Contains(outcome.kinds, unit.Unit) || (l.Config.RunID != "" && unit.RunID != l.Config.RunID) {
			return errors.New("controller awarded work outside the runner's requested selection")
		}
		key := capability.LeaseRef{Kind: unit.Unit, ID: unit.UnitID, Holder: unit.Binding.Holder, Generation: unit.Binding.Generation}
		if _, exists := active[key]; exists {
			return errors.New("controller awarded a unit already held by this runner")
		}
		active[key] = struct{}{}
		claimed++
		hostCtx := ctx
		if l.Config.FailOnFailedWork {
			hostCtx = nodehost.WithOutcomeNotify(ctx, failed.record)
		}

		slots, release := workSlots, outcome.workSlot
		if unit.Unit == capability.UnitTrigger {
			slots, release = ownerSlots, outcome.ownerSlot
			outcome.ownerSlot = nil
		} else {
			outcome.workSlot = nil
		}
		go func() {
			var slotMu sync.Mutex
			changed := func() {
				select {
				case awaitChanged <- struct{}{}:
				default:
				}
			}
			hostCtx = nodehost.WithAwaitSlot(hostCtx, func() func(context.Context) {
				slotMu.Lock()
				if release != nil {
					release()
					release = nil
				}
				changed()
				slotMu.Unlock()
				return func(ctx context.Context) {
					slotMu.Lock()
					defer slotMu.Unlock()
					if release == nil {
						select {
						case slots <- struct{}{}:
							release = func() { <-slots }
						case <-ctx.Done():
						}
					}
					changed()
				}
			})
			err := l.Host.Host(hostCtx, unit)
			slotMu.Lock()
			if release != nil {
				release()
			}
			slotMu.Unlock()
			results <- unitResult{key: key, err: err}
		}()
		return nil
	}
	var pending <-chan claimOutcome
	var updateErr error
	var updateStarted time.Time
	var updateTarget string
	checkPolicy := func() (bool, error) {
		stop, err := l.checkPolicy(claims)
		var update *runnerUpdate
		if errors.As(err, &update) {
			if updateStarted.IsZero() {
				updateStarted = time.Now()
				updateTarget = update.version
				reason := "new signed release; waiting for idle with claims live"
				if update.required {
					reason = "signed policy requires replacement"
				}
				slog.Info("runner update drain entered", "reason", reason, "elapsed", time.Duration(0), "current_version", l.Config.Version, "target_version", updateTarget, "held_units", len(active), "required", update.required)
			}
			if !update.required && (len(active) > 0 || pending != nil) {
				// safety: held hosts may await children that need this runner's claims.
				updateErr = err
				return false, nil
			}
		}
		return stop, err
	}

	defer func() {
		cancelClaims()
		if l.OnClaimsStopped != nil {
			l.OnClaimsStopped()
		}
		if pending != nil {
			runErr = errors.Join(runErr, accept(<-pending))
		}
		for len(active) > 0 {
			finish(<-results)
		}
		if !updateStarted.IsZero() {
			reason := "held work finished"
			if ctx.Err() != nil {
				reason = "caller cancelled"
			}
			slog.Info("runner update drain exited", "reason", reason, "current_version", l.Config.Version, "target_version", updateTarget, "held_units", len(active), "elapsed", time.Since(updateStarted))
		}
		runErr = errors.Join(runErr, hostErr, context.Cause(ctx))
		if l.Config.FailOnFailedWork {
			runErr = errors.Join(runErr, failed.err())
		}
	}()
	for {
		drainResults()
		if updateErr != nil && len(active) == 0 && pending == nil {
			return updateErr, nil
		}
		select {
		case <-drain:
			return nil, nil
		default:
		}
		if claims.Err() != nil || (l.Config.MaxClaims > 0 && claimed >= l.Config.MaxClaims) || l.lifecycleWait(limits.VersionIndexRefresh, idleSince, len(active) == 0) == 0 {
			return nil, nil
		}
		if transportErr != nil && time.Since(transportSince) >= 5*time.Second {
			return nil, fmt.Errorf("claim runner work: controller recovery deadline exceeded: %w", transportErr)
		}
		nextClaim := pollAt
		if backoffUntil.After(nextClaim) {
			nextClaim = backoffUntil
		}
		request := l.request()
		owners, working := len(ownerSlots), len(workSlots)
		freeSlots := l.Config.MaxConcurrent
		// safety: Owners and awaiting jobs wait for jobs, so sharing their claim slots would deadlock.
		request.Kinds = slices.DeleteFunc(request.Kinds, func(kind capability.UnitKind) bool {
			if working >= l.Config.MaxConcurrent {
				return true
			}
			used := working
			if kind == capability.UnitTrigger {
				used = owners
			}
			if used >= l.Config.MaxConcurrent {
				return true
			}
			freeSlots = min(freeSlots, l.Config.MaxConcurrent-used)
			return false
		})
		if pending == nil && len(request.Kinds) > 0 && !nextClaim.After(time.Now()) {
			if stop, err := checkPolicy(); stop {
				return err, nil
			}

			reserve := func(slots chan struct{}) func() {
				select {
				case slots <- struct{}{}:
					return func() { <-slots }
				default:
					return nil
				}
			}
			workSlot := reserve(workSlots)
			var ownerSlot func()
			if slices.Contains(request.Kinds, capability.UnitTrigger) {
				ownerSlot = reserve(ownerSlots)
			}
			request.Kinds = slices.DeleteFunc(request.Kinds, func(kind capability.UnitKind) bool {
				return workSlot == nil || kind == capability.UnitTrigger && ownerSlot == nil
			})
			if len(request.Kinds) == 0 {
				if workSlot != nil {
					workSlot()
				}
				if ownerSlot != nil {
					ownerSlot()
				}
				continue
			}
			done := make(chan claimOutcome, 1)
			pending = done
			request.FreeSlots = &freeSlots
			go func() {
				response, err := l.Controller.Claim(claims, request)
				done <- claimOutcome{response: response, err: err, kinds: request.Kinds, workSlot: workSlot, ownerSlot: ownerSlot}
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
			if controllerclient.RetryableTransportError(outcome.err) {
				transportErr = outcome.err
				if transportSince.IsZero() {
					transportSince = time.Now()
				}
				remaining := 5*time.Second - time.Since(transportSince)
				if remaining > 0 {
					delay := controllerclient.RetryBackoff(transportAttempt, l.Config.PollInterval)
					transportAttempt++
					if advised, ok := hosterr.RetryDelay(outcome.err); ok {
						delay = max(delay, advised)
					}
					// safety: a fresh poll never replays a claim whose award reply was lost.
					backoffUntil = time.Now().Add(min(delay, remaining))
					continue
				}
				return nil, fmt.Errorf("claim runner work: controller recovery deadline exceeded: %w", outcome.err)
			} else {
				transportSince = time.Time{}
				transportErr = nil
				transportAttempt = 0
			}
			if delay, ok := hosterr.RetryDelay(outcome.err); ok {
				backoffUntil = time.Now().Add(max(delay, l.Config.PollInterval))
				continue
			}
			if outcome.err != nil {
				return nil, fmt.Errorf("claim runner work: %w", outcome.err)
			}
			if outcome.response.Claim == nil {
				pollAt = time.Now().Add(l.Config.PollInterval)
				backoffUntil = time.Now().Add(min(outcome.response.PollAfter, controllerwire.MaxHonoredIdleClaimPoll))
			}
		case result := <-results:
			finish(result)
		case <-refresh.C:
			if stop, err := checkPolicy(); stop {
				return err, nil
			}
		case <-timer.C:
		case <-awaitChanged:
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
	response            controllerwire.ClaimResponse
	err                 error
	kinds               []capability.UnitKind
	workSlot, ownerSlot func()
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
	return controllerwire.ClaimRequest{
		RunnerVersion: l.Config.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, MaxConcurrent: l.Config.MaxConcurrent,
		Kinds:  slices.Clone(l.Config.Kinds),
		Labels: slices.Clone(l.Config.Labels), RunID: l.Config.RunID, Kubernetes: l.Config.Kubernetes,
	}
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

// InKubernetesPod reports whether KUBERNETES_SERVICE_HOST, which the kubelet sets in
// every container, is present in the process environment.
func InKubernetesPod() bool { return os.Getenv("KUBERNETES_SERVICE_HOST") != "" }

type failedWork struct {
	mu       sync.Mutex
	outcomes []nodehost.WorkOutcome
}

func (f *failedWork) record(outcome nodehost.WorkOutcome) {
	switch outcome.State {
	// safety: run outcomes arrive here too; RunFailed and RunCancelled share these values.
	case string(runstate.NodeFailed), string(runstate.NodeCancelled):
	default:
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomes = append(f.outcomes, outcome)
}

func (f *failedWork) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var failures []error
	seen := map[nodehost.WorkOutcome]bool{}
	for _, o := range f.outcomes {
		if seen[o] {
			continue
		}
		seen[o] = true
		subject := "run " + o.RunID
		if o.JobID != "" {
			subject = fmt.Sprintf("job %s in run %s", o.JobID, o.RunID)
		}
		failures = append(failures, fmt.Errorf("%s %s; inspect it with `sparkwing runs status --run %s`", subject, o.State, o.RunID))
	}
	return errors.Join(failures...)
}
