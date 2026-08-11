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
}

func (e *durableEffects) Apply(ctx context.Context, request EffectRequest) (EffectResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if result, ok := e.results[request.ID]; ok {
		if !reflect.DeepEqual(e.requests[request.ID], request) {
			return EffectResult{}, fmt.Errorf("conflicting request for durable effect %s", request.ID)
		}
		return result, nil
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
