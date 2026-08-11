package livechainacceptance

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type memorySessionStore struct {
	mu      sync.Mutex
	session *Session
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func (s *memorySessionStore) LoadOrCreate(_ context.Context, seed SessionSeed) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		created := Session{ID: seed.ID, SeedDigest: digestSessionSeed(seed), Events: seed.Events, Phase: SessionStarted, Version: 1}
		s.session = &created
	}
	return *s.session, nil
}

func (s *memorySessionStore) CompareAndSwap(_ context.Context, id string, expected uint64, seedDigest string, next Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil || s.session.ID != id || s.session.SeedDigest != seedDigest || s.session.Version != expected {
		return ErrSessionConflict
	}
	next.Version = expected + 1
	s.session = &next
	return nil
}

type durableEffects struct {
	mu             sync.Mutex
	script         *scriptedAcceptance
	results        map[string]EffectResult
	requests       map[string]EffectRequest
	creates        map[EffectKind]int
	loseResponse   EffectKind
	lost           bool
	malformedFault bool
	failKind       EffectKind
	reconcileCalls map[EffectKind]int
	applyCalls     map[EffectKind]int
}

func (e *durableEffects) Apply(ctx context.Context, request EffectRequest) (EffectResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.applyCalls == nil {
		e.applyCalls = make(map[EffectKind]int)
	}
	e.applyCalls[request.Kind]++
	if result, ok := e.results[request.ID]; ok {
		if !reflect.DeepEqual(e.requests[request.ID], request) {
			return EffectResult{}, fmt.Errorf("conflicting request for durable effect %s", request.ID)
		}
		return result, nil
	}
	if request.Kind == e.failKind {
		return EffectResult{}, fmt.Errorf("durable effect unavailable")
	}

	var result EffectResult
	var err error
	switch request.Kind {
	case EffectDeployA, EffectDeployB:
		result.Deployment, err = e.script.Deploy(ctx, request.Artifact)
	case EffectNotifyAcceptedA, EffectNotifyAcceptedB, EffectNotifyFailure, EffectNotifyRollback:
		result.Notification, err = e.script.Notify(ctx, request.Notification)
	case EffectInjectFailure:
		result.Fault, err = e.script.InjectFailure(ctx, request.Deployment)
		if e.malformedFault {
			result.Fault = Fault{}
		} else {
			result.Fault.ID = request.ID
		}
	case EffectRemoveFailure:
		result.Cleanup, err = e.script.RemoveFailure(ctx, request.Cleanup)
	case EffectRollback:
		result.Deployment, err = e.script.Rollback(ctx, request.Deployment)
	default:
		return EffectResult{}, fmt.Errorf("unexpected effect %q", request.Kind)
	}
	if err != nil {
		return EffectResult{}, err
	}
	if e.results == nil {
		e.results = make(map[string]EffectResult)
		e.requests = make(map[string]EffectRequest)
	}
	e.creates[request.Kind]++
	e.results[request.ID] = result
	e.requests[request.ID] = request
	lose := request.Kind == e.loseResponse && !e.lost
	if lose {
		e.lost = true
	}
	if lose {
		return EffectResult{}, fmt.Errorf("response lost after durable effect")
	}
	return result, nil
}

func TestIntermediateSessionIsValidatedBeforeEffects(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	seed := SessionSeed{ID: "acceptance-session-forged-intermediate", Events: [2]LandEvent{a, b}}
	store := &memorySessionStore{session: &Session{
		ID: seed.ID, SeedDigest: digestSessionSeed(seed), Version: 9,
		Phase: SessionRollbackIntent, Events: seed.Events,
	}}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}

	if _, err := RunSession(context.Background(), seed, durableTestDependencies(script, store, effects)); err == nil {
		t.Fatal("forged rollback intent was accepted")
	}
	effects.mu.Lock()
	defer effects.mu.Unlock()
	if len(effects.applyCalls) != 0 || len(effects.reconcileCalls) != 0 {
		t.Fatalf("forged intermediate state reached effect authority: apply=%v reconcile=%v", effects.applyCalls, effects.reconcileCalls)
	}
}

func (e *durableEffects) Reconcile(_ context.Context, request EffectRequest) (EffectResult, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reconcileCalls == nil {
		e.reconcileCalls = make(map[EffectKind]int)
	}
	e.reconcileCalls[request.Kind]++
	result, found := e.results[request.ID]
	if found && !reflect.DeepEqual(e.requests[request.ID], request) {
		return EffectResult{}, false, fmt.Errorf("conflicting request for durable effect %s", request.ID)
	}
	return result, found, nil
}

func TestFaultInjectionResponseLossAtDeadlineRecoversExactFaultBeforeCleanup(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: EffectInjectFailure}
	clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	deps := durableTestDependencies(script, store, effects)
	deps.Clock = clock
	seed := SessionSeed{ID: "acceptance-session-lost-injection", Events: [2]LandEvent{a, b}}

	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("lost injection response did not interrupt the driver")
	}
	clock.Advance(6 * time.Minute)
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("expired fault intent produced a successful proof")
	}

	wantFaultID := effectID(seed.ID, EffectInjectFailure)
	effects.mu.Lock()
	defer effects.mu.Unlock()
	if effects.creates[EffectInjectFailure] != 1 || effects.creates[EffectRemoveFailure] != 1 {
		t.Fatalf("effect creations = inject %d cleanup %d, want 1 and 1", effects.creates[EffectInjectFailure], effects.creates[EffectRemoveFailure])
	}
	if effects.reconcileCalls[EffectInjectFailure] == 0 {
		t.Fatal("expired fault intent did not reconcile the durable injection")
	}
	cleanup := effects.requests[effectID(seed.ID, EffectRemoveFailure)].Cleanup
	if cleanup.FaultID != wantFaultID || cleanup.DeploymentUID == "" || cleanup.Digest == "" {
		t.Fatalf("cleanup request = %+v, want exact recovered fault identity", cleanup)
	}
	if effects.creates[EffectRollback] != 0 {
		t.Fatal("deadline cleanup continued into rollback")
	}
}

func TestRunSessionResumesResponseLossWithoutRepeatingEffect(t *testing.T) {
	for _, lostKind := range expectedEffectKinds() {
		t.Run(string(lostKind), func(t *testing.T) {
			a, b, script := validTwoFlowScript(t)
			store := &memorySessionStore{}
			effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: lostKind}
			deps := durableTestDependencies(script, store, effects)
			seed := SessionSeed{ID: "acceptance-session-" + string(lostKind), Events: [2]LandEvent{a, b}}
			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatal("response loss did not interrupt the first driver")
			}
			proof, err := RunSession(context.Background(), seed, deps)
			if err != nil {
				t.Fatal(err)
			}
			if proof.Rollback.Commit != a.Commit || proof.Production[1].Commit != b.Commit {
				t.Fatalf("resumed proof = %+v", proof)
			}
			assertEveryEffectCreatedOnce(t, effects)
		})
	}
}

func TestConcurrentSessionDriversConvergeWithoutRepeatingEffects(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	deps := durableTestDependencies(script, store, effects)
	seed := SessionSeed{ID: "acceptance-session-concurrent", Events: [2]LandEvent{a, b}}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := RunSession(context.Background(), seed, deps)
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	assertEveryEffectCreatedOnce(t, effects)
}

func TestEffectExecutorRejectsSameIDWithChangedPayload(t *testing.T) {
	_, _, script := validTwoFlowScript(t)
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	artifact := Artifact{
		EventID: "event", Commit: testCommit, Tree: testTree, Digest: testDigest,
		VerifiedAt: time.Date(2026, 8, 11, 12, 30, 0, 0, time.UTC),
	}
	request := EffectRequest{ID: "session/deploy_a", Kind: EffectDeployA, Artifact: artifact}
	if _, err := effects.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Artifact.Tree = "99abcdef0123456789abcdef0123456789abcdef"
	if _, err := effects.Apply(context.Background(), request); err == nil {
		t.Fatal("same effect ID accepted a changed payload")
	}
	if effects.creates[EffectDeployA] != 1 {
		t.Fatalf("deploy effect creations = %d, want 1", effects.creates[EffectDeployA])
	}
}

func TestMalformedFaultReceiptTransitionsThroughCleanupBeforeFailure(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), malformedFault: true}
	seed := SessionSeed{ID: "acceptance-session-malformed-fault", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, durableTestDependencies(script, store, effects)); err == nil {
		t.Fatal("malformed fault receipt produced a successful proof")
	}
	if effects.creates[EffectRemoveFailure] != 1 {
		t.Fatalf("cleanup effects = %d, want 1", effects.creates[EffectRemoveFailure])
	}
	if effects.creates[EffectRollback] != 0 {
		t.Fatal("failed fault attempt continued into rollback")
	}
}

func TestPostFaultDeadlineTransitionsThroughCleanup(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: EffectNotifyFailure}
	clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	deps := durableTestDependencies(script, store, effects)
	deps.Clock = clock
	seed := SessionSeed{ID: "acceptance-session-deadline", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("response loss did not interrupt the driver")
	}
	clock.Advance(6 * time.Minute)
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("expired post-fault phase produced a successful proof")
	}
	if effects.creates[EffectRemoveFailure] != 1 {
		t.Fatalf("deadline cleanup effects = %d, want 1", effects.creates[EffectRemoveFailure])
	}
	if effects.creates[EffectRollback] != 0 {
		t.Fatal("deadline failure continued into rollback")
	}
}

func TestCompletedSessionIsReverifiedBeforeReturn(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	deps := durableTestDependencies(script, store, effects)
	seed := SessionSeed{ID: "acceptance-session-corrupt-complete", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.session.Proof.Rollback.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store.mu.Unlock()
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("corrupt completed proof was returned")
	}
}

func TestCompletedSessionReconcilesEveryDurableEffect(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	deps := durableTestDependencies(script, store, effects)
	seed := SessionSeed{ID: "acceptance-session-reconcile-complete", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err != nil {
		t.Fatal(err)
	}

	effects.mu.Lock()
	delete(effects.results, effectID(seed.ID, EffectRollback))
	effects.mu.Unlock()
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("completed proof was returned with a missing durable rollback effect")
	}
}

func TestCompletedSessionReauthenticatesAcceptanceObservations(t *testing.T) {
	for _, test := range []struct {
		name   string
		reject func(*scriptedAcceptance)
	}{
		{name: "artifact", reject: func(script *scriptedAcceptance) { script.rejectArtifactAuth = true }},
		{name: "health", reject: func(script *scriptedAcceptance) { script.rejectHealthAuth = true }},
		{name: "failure", reject: func(script *scriptedAcceptance) { script.rejectFailureAuth = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, b, script := validTwoFlowScript(t)
			store := &memorySessionStore{}
			effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
			deps := durableTestDependencies(script, store, effects)
			seed := SessionSeed{ID: "acceptance-session-reauth-" + test.name, Events: [2]LandEvent{a, b}}
			if _, err := RunSession(context.Background(), seed, deps); err != nil {
				t.Fatal(err)
			}
			test.reject(script)
			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatalf("completed proof survived %s authority rejection", test.name)
			}
		})
	}
}

func TestRollbackIntentAuthenticatesOriginalDeploymentBeforeEffect(t *testing.T) {
	mutations := map[string]func(*Deployment){
		"event":  func(deployment *Deployment) { deployment.EventID = "wrong" },
		"commit": func(deployment *Deployment) { deployment.Commit = "1123456789abcdef0123456789abcdef01234567" },
		"tree":   func(deployment *Deployment) { deployment.Tree = "99abcdef0123456789abcdef0123456789abcdef" },
		"digest": func(deployment *Deployment) {
			deployment.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"uid":  func(deployment *Deployment) { deployment.UID = "forged-deployment" },
		"time": func(deployment *Deployment) { deployment.DeployedAt = deployment.DeployedAt.Add(time.Minute) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			a, b, script := validTwoFlowScript(t)
			store := &memorySessionStore{}
			effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
			deps := durableTestDependencies(script, store, effects)
			seed := SessionSeed{ID: "acceptance-session-rollback-target-" + name, Events: [2]LandEvent{a, b}}
			if _, err := RunSession(context.Background(), seed, deps); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			store.session.Phase = SessionRollbackIntent
			store.session.Proof.Rollback = Deployment{}
			store.session.Proof.RollbackHealth = HealthReceipt{}
			store.session.Proof.RollbackNotice = NotificationReceipt{}
			mutate(&store.session.Proof.Deployments[0])
			store.mu.Unlock()
			effects.mu.Lock()
			effects.applyCalls = make(map[EffectKind]int)
			effects.mu.Unlock()

			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatal("forged rollback target was accepted")
			}
			effects.mu.Lock()
			defer effects.mu.Unlock()
			if effects.applyCalls[EffectRollback] != 0 {
				t.Fatal("forged rollback target reached rollback effect")
			}
		})
	}
}

func TestCleanupDeadlineBecomesDurablePageableFailure(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), failKind: EffectRemoveFailure}
	clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	deps := durableTestDependencies(script, store, effects)
	deps.Clock = clock
	seed := SessionSeed{ID: "acceptance-session-cleanup-deadline", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("cleanup failure did not interrupt the driver")
	}
	clock.Advance(6 * time.Minute)
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("expired cleanup produced a successful proof")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.session.Phase != SessionCleanupFailed || store.session.Proof.Fault.ID == "" || store.session.TerminalError == "" {
		t.Fatalf("cleanup deadline state = %+v", *store.session)
	}
}

func TestCleanupResponseLossAtDeadlineReconcilesNoResidue(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: EffectRemoveFailure}
	clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	deps := durableTestDependencies(script, store, effects)
	deps.Clock = clock
	seed := SessionSeed{ID: "acceptance-session-cleanup-response-loss", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("lost cleanup response did not interrupt the driver")
	}
	clock.Advance(6 * time.Minute)
	proof, err := RunSession(context.Background(), seed, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Cleanup.NoResidue || proof.Cleanup.FaultID != effectID(seed.ID, EffectInjectFailure) {
		t.Fatalf("cleanup proof = %+v", proof.Cleanup)
	}
	effects.mu.Lock()
	defer effects.mu.Unlock()
	if effects.creates[EffectRemoveFailure] != 1 || effects.reconcileCalls[EffectRemoveFailure] == 0 {
		t.Fatalf("cleanup create/reconcile = %d/%d, want 1/positive", effects.creates[EffectRemoveFailure], effects.reconcileCalls[EffectRemoveFailure])
	}
}

func TestExpiredFaultIntentRejectsMalformedReconciledReceipt(t *testing.T) {
	mutations := map[string]func(*Fault){
		"id":             func(fault *Fault) { fault.ID = "wrong" },
		"event":          func(fault *Fault) { fault.EventID = "wrong" },
		"deployment_uid": func(fault *Fault) { fault.DeploymentUID = "wrong" },
		"digest": func(fault *Fault) {
			fault.Digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
		"time": func(fault *Fault) { fault.InjectedAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			a, b, script := validTwoFlowScript(t)
			store := &memorySessionStore{}
			effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: EffectInjectFailure}
			clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
			deps := durableTestDependencies(script, store, effects)
			deps.Clock = clock
			seed := SessionSeed{ID: "acceptance-session-corrupt-reconciled-fault-" + name, Events: [2]LandEvent{a, b}}
			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatal("lost injection response did not interrupt the driver")
			}
			effects.mu.Lock()
			result := effects.results[effectID(seed.ID, EffectInjectFailure)]
			mutate(&result.Fault)
			effects.results[effectID(seed.ID, EffectInjectFailure)] = result
			effects.mu.Unlock()
			clock.Advance(6 * time.Minute)
			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatal("malformed reconciled fault was accepted")
			}
			effects.mu.Lock()
			defer effects.mu.Unlock()
			if effects.creates[EffectRemoveFailure] != 0 {
				t.Fatal("malformed reconciled fault was credited to cleanup")
			}
		})
	}
}

func durableTestDependencies(script *scriptedAcceptance, store SessionStore, effects EffectExecutor) DurableDependencies {
	return DurableDependencies{
		Authority: testAuthority{}, Production: script, Artifacts: script, Health: script,
		Faults: script, Sessions: store, Effects: effects,
		Clock: fixedClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)},
	}
}

func expectedEffectKinds() []EffectKind {
	return []EffectKind{
		EffectDeployA, EffectNotifyAcceptedA, EffectDeployB, EffectNotifyAcceptedB,
		EffectInjectFailure, EffectNotifyFailure, EffectRemoveFailure, EffectRollback, EffectNotifyRollback,
	}
}

func assertEveryEffectCreatedOnce(t *testing.T, effects *durableEffects) {
	t.Helper()
	if len(effects.creates) != len(expectedEffectKinds()) {
		t.Fatalf("created effect kinds = %d, want %d", len(effects.creates), len(expectedEffectKinds()))
	}
	for _, kind := range expectedEffectKinds() {
		if effects.creates[kind] != 1 {
			t.Fatalf("effect %s created %d times, want 1", kind, effects.creates[kind])
		}
	}
}

func validTwoFlowScript(t *testing.T) (LandEvent, LandEvent, *scriptedAcceptance) {
	t.Helper()
	a := validEvent()
	b := validEvent()
	b.EventID = "land-event-2"
	b.ParentCommit = a.Commit
	b.Commit = "2123456789abcdef0123456789abcdef01234567"
	b.Tree = "29abcdef0123456789abcdef0123456789abcdef"
	b.GitLedgerID = "git-ledger-2"
	b.LandRecordID = "land-record-2"
	b.LandSequence = a.LandSequence + 1
	b.ChainLedgerID = "chain-ledger-2"
	b.ChainBasePosition = 700
	b.LandedAt = a.LandedAt.Add(30 * time.Second)
	b.Deadline = b.LandedAt.Add(5 * time.Minute)
	return a, b, &scriptedAcceptance{
		chains: map[string][]Acknowledgement{a.EventID: validChain(t, a), b.EventID: validChain(t, b)},
	}
}
