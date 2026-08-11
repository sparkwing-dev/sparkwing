package livechainacceptance

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type memorySessionStore struct {
	mu      sync.Mutex
	session *Session
}

func (s *memorySessionStore) LoadOrCreate(_ context.Context, seed SessionSeed) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		created := Session{ID: seed.ID, Events: seed.Events, Phase: SessionStarted, Version: 1}
		s.session = &created
	}
	return *s.session, nil
}

func (s *memorySessionStore) CompareAndSwap(_ context.Context, expected uint64, next Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil || s.session.Version != expected {
		return ErrSessionConflict
	}
	next.Version = expected + 1
	s.session = &next
	return nil
}

type durableEffects struct {
	mu           sync.Mutex
	script       *scriptedAcceptance
	results      map[string]EffectResult
	creates      map[EffectKind]int
	loseResponse EffectKind
	lost         bool
}

func (e *durableEffects) Apply(ctx context.Context, request EffectRequest) (EffectResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if result, ok := e.results[request.ID]; ok {
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
		result.Fault.ID = request.ID
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
	}
	e.creates[request.Kind]++
	e.results[request.ID] = result
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
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{
		script: script, creates: make(map[EffectKind]int), loseResponse: EffectNotifyAcceptedB,
	}
	deps := DurableDependencies{
		Authority: testAuthority{}, Production: script, Artifacts: script, Health: script,
		Faults: script, Sessions: store, Effects: effects,
	}
	seed := SessionSeed{ID: "acceptance-session-1", Events: [2]LandEvent{a, b}}
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
	for kind, creates := range effects.creates {
		if creates != 1 {
			t.Fatalf("effect %s created %d times", kind, creates)
		}
	}
}

func TestConcurrentSessionDriversConvergeWithoutRepeatingEffects(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	deps := DurableDependencies{
		Authority: testAuthority{}, Production: script, Artifacts: script, Health: script,
		Faults: script, Sessions: store, Effects: effects,
	}
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
	for kind, creates := range effects.creates {
		if creates != 1 {
			t.Fatalf("effect %s created %d times", kind, creates)
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
	b.LandedAt = a.LandedAt.Add(30 * time.Second)
	b.Deadline = b.LandedAt.Add(5 * time.Minute)
	return a, b, &scriptedAcceptance{
		chains: map[string][]Acknowledgement{a.EventID: validChain(t, a), b.EventID: validChain(t, b)},
	}
}
