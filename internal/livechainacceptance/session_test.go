package livechainacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

type memorySessionStore struct {
	mu            sync.Mutex
	session       *Session
	highWatermark uint64
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

func (s *memorySessionStore) LoadOrCreate(ctx context.Context, seed SessionSeed, factory InitialSessionFactory, verifier SessionVerifier) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		initial, err := factory(ctx)
		if err != nil {
			return Session{}, err
		}
		if err := verifier.Verify(ctx, initial); err != nil {
			return Session{}, err
		}
		s.session = &initial
		s.highWatermark = initial.Version
	}
	if s.session.Version < s.highWatermark {
		return Session{}, fmt.Errorf("durable session tail was replayed below its high watermark")
	}
	return *s.session, nil
}

func (s *memorySessionStore) CompareAndSwap(ctx context.Context, id string, expected uint64, expectedDigest, seedDigest string, next Session, verifier SessionVerifier) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil || s.session.ID != id || s.session.SeedDigest != seedDigest || s.session.Version != expected || s.session.StateSeal.Digest != expectedDigest || next.Version != expected+1 || next.PreviousStateDigest != expectedDigest {
		return ErrSessionConflict
	}
	if err := verifier.Verify(ctx, next); err != nil {
		return err
	}
	unsigned := next
	unsigned.StateSeal = StateSeal{}
	if err := validateSuccessorSealRequest(*s.session, unsigned, next.StateSeal.SignedAt); err != nil {
		return err
	}
	s.session = &next
	s.highWatermark = next.Version
	return nil
}

type testSessionAuthenticator struct {
	secret string
	now    time.Time
	clock  Clock
}

type testSessionVerifier struct{ auth testSessionAuthenticator }

func (v testSessionVerifier) Verify(ctx context.Context, session Session) error {
	return v.auth.Verify(ctx, session)
}

const (
	testSessionDomain    = "sparkwing/livechainacceptance/session/v1"
	testSessionSchema    = "v1"
	testSessionKeyID     = "test-session-key"
	testSessionAlgorithm = "test-sha256"
)

func (a testSessionAuthenticator) sign(digest string) string {
	sum := sha256.Sum256([]byte("live-chain/session-test/v1\x00" + a.secret + "\x00" + digest))
	return hex.EncodeToString(sum[:])
}

func (a testSessionAuthenticator) trustedNow() time.Time {
	if a.clock != nil {
		return a.clock.Now()
	}
	if !a.now.IsZero() {
		return a.now
	}
	return time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
}

func (a testSessionAuthenticator) InitialFactory(seed SessionSeed) InitialSessionFactory {
	var once sync.Once
	var sealed Session
	var sealErr error
	return func(_ context.Context) (Session, error) {
		once.Do(func() {
			initial := initialSession(seed)
			initial.PhaseDeadline = a.trustedNow().Add(5 * time.Minute)
			if err := validateInitialSealRequest(seed, initial); err != nil {
				sealErr = err
				return
			}
			initial.StateSeal = StateSeal{Domain: testSessionDomain, SchemaVersion: testSessionSchema, KeyID: testSessionKeyID, Algorithm: testSessionAlgorithm, SignedAt: a.trustedNow()}
			sealed, sealErr = sessionWithStateDigest(initial)
			if sealErr == nil {
				sealed.StateSeal.Signature = a.sign(sealed.StateSeal.Digest)
			}
		})
		return sealed, sealErr
	}
}

func (a testSessionAuthenticator) SealSuccessor(ctx context.Context, current, next Session) (Session, error) {
	if err := a.Verify(ctx, current); err != nil {
		return Session{}, err
	}
	if err := validateSuccessorSealRequest(current, next, a.trustedNow()); err != nil {
		return Session{}, err
	}
	next.StateSeal = StateSeal{Domain: testSessionDomain, SchemaVersion: testSessionSchema, KeyID: testSessionKeyID, Algorithm: testSessionAlgorithm, SignedAt: a.trustedNow()}
	sealed, err := sessionWithStateDigest(next)
	if err != nil {
		return Session{}, err
	}
	sealed.StateSeal.Signature = a.sign(sealed.StateSeal.Digest)
	return sealed, nil
}

func (a testSessionAuthenticator) Verify(_ context.Context, session Session) error {
	if session.StateSeal.Domain != testSessionDomain || session.StateSeal.SchemaVersion != testSessionSchema || session.StateSeal.KeyID != testSessionKeyID || session.StateSeal.Algorithm != testSessionAlgorithm || session.StateSeal.SignedAt.IsZero() {
		return fmt.Errorf("session state seal authority is unknown")
	}
	digest, err := digestSessionState(session)
	if err != nil {
		return err
	}
	if digest != session.StateSeal.Digest {
		return fmt.Errorf("session state canonical digest mismatch")
	}
	if session.StateSeal.Signature != a.sign(session.StateSeal.Digest) {
		return fmt.Errorf("session state signature mismatch")
	}
	return nil
}

func sealedInitialForTest(t *testing.T, auth testSessionAuthenticator, seed SessionSeed) Session {
	t.Helper()
	initial, err := auth.InitialFactory(seed)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return initial
}

func TestSessionAuthenticatorRefusesInvalidSuccessorsBeforeSigning(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "typed-session-signing", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	initial := sealedInitialForTest(t, auth, seed)
	valid := initial
	valid.Version++
	valid.PreviousStateDigest = initial.StateSeal.Digest
	valid.StateSeal = StateSeal{}
	valid.Phase = SessionAPrepared
	valid.PhaseDeadline = time.Date(2026, 8, 11, 14, 5, 0, 0, time.UTC)
	if sealed, err := auth.SealSuccessor(context.Background(), initial, valid); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("evidence-less ordinary preparation reached a coordinator signature")
	}
	valid.Proof.Events = seed.Events
	failed := initial
	failed.Version++
	failed.PreviousStateDigest = initial.StateSeal.Digest
	failed.StateSeal = StateSeal{}
	failed.Phase = SessionFailed
	if sealed, err := auth.SealSuccessor(context.Background(), initial, failed); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("failure without terminal error reached a coordinator signature")
	}

	mutations := map[string]func(*Session){
		"cross_session": func(next *Session) { next.ID = "another-session" },
		"cross_seed": func(next *Session) {
			next.SeedDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		},
		"cross_events":  func(next *Session) { next.Events[0].Commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
		"replay":        func(next *Session) { next.Version = initial.Version },
		"skip_version":  func(next *Session) { next.Version = initial.Version + 2 },
		"wrong_parent":  func(next *Session) { next.PreviousStateDigest = genesisSessionStateDigest() },
		"illegal_phase": func(next *Session) { next.Phase = SessionComplete },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			next := valid
			mutate(&next)
			sealed, err := auth.SealSuccessor(context.Background(), initial, next)
			if err == nil {
				t.Fatal("invalid successor reached a signature")
			}
			if sealed.StateSeal.Signature != "" {
				t.Fatal("rejected successor carries a coordinator signature")
			}
		})
	}

	tamperedCurrent := initial
	tamperedCurrent.Phase = SessionAPrepared
	if sealed, err := auth.SealSuccessor(context.Background(), tamperedCurrent, valid); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("tampered authenticated current state reached a successor signature")
	}
}

func TestSessionAuthenticatorRefusesProofErasureAndCleanupBypass(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	auth := testSessionAuthenticator{secret: "test-only"}
	seed := SessionSeed{ID: "typed-session-policy", Events: [2]LandEvent{a, b}}
	initial := sealedInitialForTest(t, auth, seed)
	prepared := initial
	prepared.Version++
	prepared.PreviousStateDigest = initial.StateSeal.Digest
	prepared.StateSeal = StateSeal{}
	prepared.Phase = SessionAPrepared
	prepared.PhaseDeadline = time.Date(2026, 8, 11, 14, 5, 0, 0, time.UTC)
	deps := durableTestDependencies(script, nil, nil)
	if err := prepareFlow(context.Background(), deps, &prepared, 0); err != nil {
		t.Fatal(err)
	}
	farFuture := prepared
	farFuture.PhaseDeadline = auth.trustedNow().Add(24 * time.Hour)
	if sealed, err := auth.SealSuccessor(context.Background(), initial, farFuture); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("far-future first deadline reached a coordinator signature")
	}
	prepared, err := auth.SealSuccessor(context.Background(), initial, prepared)
	if err != nil {
		t.Fatal(err)
	}

	erased := prepared
	erased.Version++
	erased.PreviousStateDigest = prepared.StateSeal.Digest
	erased.StateSeal = StateSeal{}
	erased.Phase = SessionADeployIntent
	erased.Proof.Production[0] = ProductionReceipt{}
	if sealed, err := auth.SealSuccessor(context.Background(), prepared, erased); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("proof erasure reached a coordinator signature")
	}
	for name, mutate := range map[string]func(*Session){
		"terminal_error": func(next *Session) { next.TerminalError = "fabricated terminal error" },
		"deadline":       func(next *Session) { next.PhaseDeadline = prepared.PhaseDeadline.Add(6 * time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			next := prepared
			next.Version++
			next.PreviousStateDigest = prepared.StateSeal.Digest
			next.StateSeal = StateSeal{}
			next.Phase = SessionADeployIntent
			mutate(&next)
			if sealed, err := auth.SealSuccessor(context.Background(), prepared, next); err == nil || sealed.StateSeal.Signature != "" {
				t.Fatal("unauthorized session metadata rewrite reached a coordinator signature")
			}
		})
	}
	premature := prepared
	premature.StateSeal = StateSeal{}
	premature.Version = initial.Version + 1
	premature.PreviousStateDigest = initial.StateSeal.Digest
	premature.Proof.Deployments[0] = Deployment{UID: "premature"}
	if sealed, err := auth.SealSuccessor(context.Background(), initial, premature); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("premature proof field reached a coordinator signature")
	}

	cleanupStore := &memorySessionStore{}
	cleanupEffects := &durableEffects{script: script, creates: make(map[EffectKind]int), failKind: EffectRemoveFailure}
	cleanupDeps := durableTestDependencies(script, cleanupStore, cleanupEffects)
	cleanupSeed := SessionSeed{ID: "typed-session-cleanup-bypass", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), cleanupSeed, cleanupDeps); err == nil {
		t.Fatal("cleanup failure did not stop the session")
	}
	cleanupStore.mu.Lock()
	cleanupIntent := *cleanupStore.session
	cleanupStore.mu.Unlock()
	evidenceless := cleanupIntent
	evidenceless.Version++
	evidenceless.PreviousStateDigest = cleanupIntent.StateSeal.Digest
	evidenceless.StateSeal = StateSeal{}
	evidenceless.Phase = SessionCleaned
	if sealed, err := auth.SealSuccessor(context.Background(), cleanupIntent, evidenceless); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("evidence-less cleanup reached a coordinator signature")
	}
	bypass := cleanupIntent
	bypass.Version++
	bypass.PreviousStateDigest = cleanupIntent.StateSeal.Digest
	bypass.StateSeal = StateSeal{}
	bypass.Phase = SessionFailed
	if sealed, err := auth.SealSuccessor(context.Background(), cleanupIntent, bypass); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("terminal cleanup bypass reached a coordinator signature")
	}

	rollbackStore := &memorySessionStore{}
	rollbackEffects := &durableEffects{script: script, creates: make(map[EffectKind]int), failKind: EffectRollback}
	rollbackDeps := durableTestDependencies(script, rollbackStore, rollbackEffects)
	rollbackSeed := SessionSeed{ID: "typed-session-cleanup-erasure", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), rollbackSeed, rollbackDeps); err == nil {
		t.Fatal("rollback failure did not stop the session")
	}
	rollbackStore.mu.Lock()
	rollbackIntent := *rollbackStore.session
	rollbackStore.mu.Unlock()
	removed := rollbackIntent
	removed.Version++
	removed.PreviousStateDigest = rollbackIntent.StateSeal.Digest
	removed.StateSeal = StateSeal{}
	removed.Phase = SessionFailed
	removed.Proof.Cleanup = CleanupReceipt{}
	if sealed, err := auth.SealSuccessor(context.Background(), rollbackIntent, removed); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("cleanup proof erasure reached a coordinator signature")
	}
}

func TestSessionStateRejectsUnknownSealAndLowerTailReplay(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	auth := testSessionAuthenticator{secret: "test-only"}
	seed := SessionSeed{ID: "typed-session-tail", Events: [2]LandEvent{a, b}}
	initial := sealedInitialForTest(t, auth, seed)
	for _, mutate := range []func(*Session){
		func(session *Session) { session.StateSeal.Domain = "wrong-domain" },
		func(session *Session) { session.StateSeal.SchemaVersion = "v2" },
		func(session *Session) { session.StateSeal.KeyID = "unknown-key" },
		func(session *Session) { session.StateSeal.Algorithm = "unknown-algorithm" },
	} {
		candidate := initial
		mutate(&candidate)
		candidateWithDigest, err := sessionWithStateDigest(candidate)
		if err != nil {
			t.Fatal(err)
		}
		candidate = candidateWithDigest
		candidate.StateSeal.Signature = auth.sign(candidate.StateSeal.Digest)
		if err := verifySessionState(context.Background(), auth, candidate); err == nil {
			t.Fatal("unknown seal authority was accepted")
		}
	}

	store := &memorySessionStore{session: &initial, highWatermark: initial.Version + 1}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	if _, err := RunSession(context.Background(), seed, durableTestDependencies(script, store, effects)); err == nil {
		t.Fatal("valid lower-version session tail was replayed")
	}
	effects.mu.Lock()
	defer effects.mu.Unlock()
	if len(effects.applyCalls) != 0 || len(effects.reconcileCalls) != 0 {
		t.Fatal("lower-tail replay reached effect authority")
	}
}

func TestSessionStoreAcceptsOneConcurrentSuccessor(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	auth := testSessionAuthenticator{secret: "test-only"}
	seed := SessionSeed{ID: "typed-session-concurrent", Events: [2]LandEvent{a, b}}
	initial := sealedInitialForTest(t, auth, seed)
	store := &memorySessionStore{session: &initial, highWatermark: initial.Version}
	makeSuccessor := func(terminalError string) Session {
		next := initial
		next.Version++
		next.PreviousStateDigest = initial.StateSeal.Digest
		next.StateSeal = StateSeal{}
		next.Phase = SessionFailed
		next.TerminalError = terminalError
		next.PhaseDeadline = time.Time{}
		sealed, sealErr := auth.SealSuccessor(context.Background(), initial, next)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return sealed
	}
	successors := []Session{makeSuccessor("first terminal outcome"), makeSuccessor("competing terminal outcome")}
	results := make(chan error, len(successors))
	for _, successor := range successors {
		go func(next Session) {
			results <- store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, testSessionVerifier{auth: auth})
		}(successor)
	}
	successes := 0
	for range successors {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successors accepted = %d, want 1", successes)
	}
}

func TestSessionStoreRejectsUnsignedSuccessorInsideCAS(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	auth := testSessionAuthenticator{secret: "test-only"}
	seed := SessionSeed{ID: "typed-session-unsigned-cas", Events: [2]LandEvent{a, b}}
	initial := sealedInitialForTest(t, auth, seed)
	store := &memorySessionStore{session: &initial, highWatermark: initial.Version}
	next := initial
	next.Version++
	next.PreviousStateDigest = initial.StateSeal.Digest
	next.StateSeal = StateSeal{}
	next.Phase = SessionFailed
	next.TerminalError = "unsigned terminal outcome"
	if err := store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, testSessionVerifier{auth: auth}); err == nil {
		t.Fatal("trusted append accepted an unsigned successor")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.session.Version != initial.Version {
		t.Fatal("unsigned successor advanced the durable tail")
	}
}

func TestSessionStoreCapabilitiesExcludeSuccessorSigning(t *testing.T) {
	verifierType := reflect.TypeOf((*SessionVerifier)(nil)).Elem()
	if verifierType.NumMethod() != 1 || verifierType.Method(0).Name != "Verify" {
		t.Fatalf("store verifier capability exposes methods beyond Verify: %v", verifierType)
	}
	factoryType := reflect.TypeOf(InitialSessionFactory(nil))
	if factoryType.NumMethod() != 0 {
		t.Fatalf("initial factory exposes coordinator methods: %v", factoryType)
	}
	if _, exposed := verifierType.MethodByName("SealSuccessor"); exposed {
		t.Fatal("store verifier capability exposes successor signing")
	}
	verifier := testSessionVerifier{auth: testSessionAuthenticator{secret: "test-only"}}
	if _, exposed := any(verifier).(SessionSigner); exposed {
		t.Fatal("store verifier dynamic value exposes successor signing")
	}
	if _, exposed := any(verifier).(SessionAuthenticator); exposed {
		t.Fatal("store verifier dynamic value exposes the combined authenticator")
	}
}

func TestSessionSigningV1SchemaIsExplicit(t *testing.T) {
	assertFields := func(value any, want []string) {
		t.Helper()
		typeOf := reflect.TypeOf(value)
		got := make([]string, typeOf.NumField())
		for index := range got {
			got[index] = typeOf.Field(index).Name
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s fields = %v, want %v; bump the signing schema explicitly", typeOf.Name(), got, want)
		}
	}
	assertFields(Session{}, []string{"ID", "SeedDigest", "Version", "PreviousStateDigest", "StateSeal", "Phase", "Events", "Proof", "TerminalError", "PhaseDeadline"})
	assertFields(StateSeal{}, []string{"Domain", "SchemaVersion", "KeyID", "Algorithm", "SignedAt", "Digest", "Signature"})
	assertFields(LandEvent{}, []string{"EventID", "Repository", "DestinationRef", "Commit", "ParentCommit", "Tree", "CertificationID", "ArtifactManifestDigest", "TrustManifestDigest", "SourceDigest", "GitLedgerID", "LandRecordID", "LandLedgerID", "LandSequence", "ChainLedgerID", "ChainBasePosition", "LandedAt", "Deadline"})
	assertFields(ProductionReceipt{}, []string{"EventID", "Commit", "Tree", "TerminalStage", "TerminalDigest", "SuccessAt", "LandToSuccess", "Acknowledgements", "Authority"})
	assertFields(Acknowledgement{}, []string{"Stage", "Digest", "EventID", "PreviousSelectedDigest", "StageEvidenceDigest", "StageAt", "Repository", "DestinationRef", "Commit", "Tree", "CertificationID", "ArtifactManifestDigest", "TrustManifestDigest", "LandedAt", "Deadline", "AuthorityDomain", "SignerKeyID", "ImmutableVersion", "LedgerPosition", "Signature", "DiscordDelivery"})
	assertFields(AuthorityReceipt{}, []string{"Domain", "LedgerID", "SignerKeyID", "ImmutableVersion", "LedgerPosition", "VerifiedDigest", "InclusionDigest", "Signature"})
	assertFields(DiscordDelivery{}, []string{"BridgeIdentity", "RequestID", "PayloadDigest", "HTTPStatus", "DeliveredAt"})
	assertFields(Artifact{}, []string{"EventID", "Commit", "Tree", "Digest", "VerifiedAt"})
	assertFields(Deployment{}, []string{"EventID", "Commit", "Tree", "Digest", "UID", "DeployedAt"})
	assertFields(HealthReceipt{}, []string{"EventID", "Commit", "Tree", "Digest", "DeploymentUID", "Healthy", "ObservedAt"})
	assertFields(NotificationReceipt{}, []string{"NotificationRequest", "BridgeIdentity", "RequestID", "PayloadDigest", "HTTPStatus", "DeliveredAt"})
	assertFields(Fault{}, []string{"EventID", "ID", "DeploymentUID", "Digest", "InjectedAt"})
	assertFields(FailureReceipt{}, []string{"FaultID", "DeploymentUID", "Digest", "Unhealthy", "ObservedAt"})
	assertFields(CleanupReceipt{}, []string{"FaultID", "EventID", "DeploymentUID", "Digest", "NoResidue", "RemovedAt"})
}

func TestSessionSigningV1CoversEveryLeaf(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	seed := SessionSeed{ID: "signed-leaf-coverage", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, durableTestDependencies(script, store, effects)); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	state := *store.session
	store.mu.Unlock()
	baseline, err := digestSessionState(state)
	if err != nil {
		t.Fatal(err)
	}
	mutated := 0
	walkMutableLeaves(reflect.ValueOf(&state).Elem(), "Session", func(path string, leaf reflect.Value) {
		before := reflect.New(leaf.Type()).Elem()
		before.Set(leaf)
		mutateSigningLeaf(leaf)
		got, digestErr := digestSessionState(state)
		leaf.Set(before)
		if digestErr != nil {
			t.Fatalf("digest %s: %v", path, digestErr)
		}
		excluded := path == "Session.StateSeal.Digest" || path == "Session.StateSeal.Signature"
		if excluded && got != baseline {
			t.Fatalf("seal output field %s changed its own digest", path)
		}
		if !excluded && got == baseline {
			t.Fatalf("signed v1 projection omits %s", path)
		}
		mutated++
	})
	if mutated < 200 {
		t.Fatalf("signed v1 leaf coverage visited %d fields, want at least 200", mutated)
	}
}

func walkMutableLeaves(value reflect.Value, path string, visit func(string, reflect.Value)) {
	if value.Type() == reflect.TypeOf(time.Time{}) {
		visit(path, value)
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			walkMutableLeaves(value.Field(index), path+"."+value.Type().Field(index).Name, visit)
		}
	case reflect.Array, reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			walkMutableLeaves(value.Index(index), fmt.Sprintf("%s[%d]", path, index), visit)
		}
	case reflect.Pointer:
		if !value.IsNil() {
			walkMutableLeaves(value.Elem(), path+"*", visit)
		}
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		visit(path, value)
	}
}

func mutateSigningLeaf(value reflect.Value) {
	if value.Type() == reflect.TypeOf(time.Time{}) {
		current := value.Interface().(time.Time)
		if current.IsZero() {
			value.Set(reflect.ValueOf(time.Unix(1, 0).UTC()))
		} else {
			value.Set(reflect.ValueOf(current.Add(time.Nanosecond)))
		}
		return
	}
	switch value.Kind() {
	case reflect.String:
		value.SetString(value.String() + "#")
	case reflect.Bool:
		value.SetBool(!value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(value.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(value.Uint() + 1)
	}
}

func TestSessionTransitionAuthorityIsClosed(t *testing.T) {
	const expectedEdges = 50
	if len(sessionTransitionRules) != expectedEdges {
		t.Fatalf("transition rules = %d, want %d", len(sessionTransitionRules), expectedEdges)
	}
	outgoing := make(map[SessionPhase]int)
	for edge, rule := range sessionTransitionRules {
		outgoing[edge.from]++
		seen := make(map[proofField]struct{}, len(rule.proofDelta)+len(rule.optionalProof))
		for _, field := range append(append([]proofField{}, rule.proofDelta...), rule.optionalProof...) {
			if _, duplicate := seen[field]; duplicate {
				t.Fatalf("edge %s -> %s repeats proof field %s", edge.from, edge.to, field)
			}
			seen[field] = struct{}{}
		}
		if rule.maySetTerminalErr && edge.to != SessionFailed && edge.to != SessionCleanupIntent && edge.to != SessionCleanupFailed {
			t.Fatalf("edge %s -> %s may introduce an unrelated terminal error", edge.from, edge.to)
		}
	}
	for _, phase := range []SessionPhase{
		SessionStarted, SessionAPrepared, SessionADeployIntent, SessionADeployed, SessionAHealthy, SessionANotifyIntent,
		SessionAAccepted, SessionBPrepared, SessionBDeployIntent, SessionBDeployed, SessionBHealthy, SessionBNotifyIntent,
		SessionBAccepted, SessionFaultIntent, SessionFaultInjected, SessionFailureObserved, SessionFailureNotifyIntent,
		SessionFailureNotified, SessionCleanupIntent, SessionCleanupFailed, SessionCleaned, SessionRollbackIntent,
		SessionRolledBack, SessionRollbackHealthy, SessionRollbackNotifyIntent,
	} {
		if outgoing[phase] == 0 {
			t.Fatalf("nonterminal phase %s has no transition authority", phase)
		}
	}
	if outgoing[SessionComplete] != 0 || outgoing[SessionFailed] != 0 {
		t.Fatal("terminal phase has an outgoing transition")
	}
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

func TestSessionRejectsStructurallyValidUnsignedSuccessorBeforeEffects(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
	deps := durableTestDependencies(script, store, effects)
	seed := SessionSeed{ID: "acceptance-session-forged-successor", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.session.Phase = SessionRollbackIntent
	store.session.Proof.Rollback = Deployment{}
	store.session.Proof.RollbackHealth = HealthReceipt{}
	store.session.Proof.RollbackNotice = NotificationReceipt{}
	store.mu.Unlock()
	effects.mu.Lock()
	effects.applyCalls = make(map[EffectKind]int)
	effects.reconcileCalls = make(map[EffectKind]int)
	effects.mu.Unlock()

	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("unsigned structurally valid successor was accepted")
	}
	effects.mu.Lock()
	defer effects.mu.Unlock()
	if len(effects.applyCalls) != 0 || len(effects.reconcileCalls) != 0 {
		t.Fatalf("unsigned successor reached effect authority: apply=%v reconcile=%v", effects.applyCalls, effects.reconcileCalls)
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
	setDurableTestClock(&deps, clock)
	seed := SessionSeed{ID: "acceptance-session-lost-injection", Events: [2]LandEvent{a, b}}

	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("lost injection response did not interrupt the driver")
	}
	clock.Advance(6 * time.Minute)
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("expired fault intent produced a successful proof")
	} else {
		t.Logf("expired fault intent result: %v", err)
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

func TestEveryEffectIntentAuthenticatesItsImmediatePrerequisite(t *testing.T) {
	cases := []struct {
		name   string
		phase  SessionPhase
		mutate func(*Session, *durableEffects)
	}{
		{name: "deploy_a", phase: SessionADeployIntent, mutate: func(session *Session, _ *durableEffects) {
			session.Proof.Production[0].Acknowledgements[0].Signature += "bad"
		}},
		{name: "notify_a_deploy", phase: SessionANotifyIntent, mutate: deleteEffectResult(EffectDeployA)},
		{name: "notify_a_health", phase: SessionANotifyIntent, mutate: func(session *Session, _ *durableEffects) { session.Proof.Health[0].DeploymentUID += "bad" }},
		{name: "deploy_b", phase: SessionBDeployIntent, mutate: func(session *Session, _ *durableEffects) {
			session.Proof.Production[1].Acknowledgements[0].Signature += "bad"
		}},
		{name: "notify_b_deploy", phase: SessionBNotifyIntent, mutate: deleteEffectResult(EffectDeployB)},
		{name: "notify_b_health", phase: SessionBNotifyIntent, mutate: func(session *Session, _ *durableEffects) { session.Proof.Health[1].DeploymentUID += "bad" }},
		{name: "fault", phase: SessionFaultIntent, mutate: deleteEffectResult(EffectNotifyAcceptedB)},
		{name: "failure_notify", phase: SessionFailureNotifyIntent, mutate: deleteEffectResult(EffectInjectFailure)},
		{name: "cleanup_after_notice", phase: SessionCleanupIntent, mutate: deleteEffectResult(EffectNotifyFailure)},
		{name: "cleanup_after_fault", phase: SessionCleanupIntent, mutate: func(session *Session, effects *durableEffects) {
			session.Proof.FailureNotice = NotificationReceipt{}
			deleteEffectResult(EffectInjectFailure)(session, effects)
		}},
		{name: "cleanup_after_malformed_fault", phase: SessionCleanupIntent, mutate: func(session *Session, effects *durableEffects) {
			session.Proof.Fault = Fault{}
			session.Proof.Failure = FailureReceipt{}
			session.Proof.FailureNotice = NotificationReceipt{}
			deleteEffectResult(EffectNotifyAcceptedB)(session, effects)
		}},
		{name: "rollback_cleanup", phase: SessionRollbackIntent, mutate: deleteEffectResult(EffectRemoveFailure)},
		{name: "rollback_original_deploy", phase: SessionRollbackIntent, mutate: func(session *Session, effects *durableEffects) {
			deleteEffectResult(EffectDeployA)(session, effects)
		}},
		{name: "rollback_notify", phase: SessionRollbackNotifyIntent, mutate: deleteEffectResult(EffectRollback)},
		{name: "rollback_health", phase: SessionRollbackNotifyIntent, mutate: func(session *Session, _ *durableEffects) { session.Proof.RollbackHealth.DeploymentUID += "bad" }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			a, b, script := validTwoFlowScript(t)
			store := &memorySessionStore{}
			effects := &durableEffects{script: script, creates: make(map[EffectKind]int)}
			deps := durableTestDependencies(script, store, effects)
			seed := SessionSeed{ID: "intent-authority-" + testCase.name, Events: [2]LandEvent{a, b}}
			if _, err := RunSession(context.Background(), seed, deps); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			session := *store.session
			store.mu.Unlock()
			session.Phase = testCase.phase
			testCase.mutate(&session, effects)
			if err := validateIntentAuthority(context.Background(), deps, session); err == nil {
				t.Fatal("corrupt immediate prerequisite was accepted")
			}
		})
	}
}

func deleteEffectResult(kind EffectKind) func(*Session, *durableEffects) {
	return func(session *Session, effects *durableEffects) {
		effects.mu.Lock()
		delete(effects.results, effectID(session.ID, kind))
		effects.mu.Unlock()
	}
}

type rejectingArtifactAuthenticator struct{ ArtifactVerifier }

func (rejectingArtifactAuthenticator) AuthenticateArtifact(context.Context, LandEvent, Artifact) error {
	return fmt.Errorf("artifact authority rejected receipt")
}

type rejectingFailureAuthenticator struct{ FaultController }

func (rejectingFailureAuthenticator) AuthenticateFailure(context.Context, Fault, FailureReceipt) error {
	return fmt.Errorf("failure authority rejected receipt")
}

func TestRunSessionAuthenticatesIntentBeforeTargetEffect(t *testing.T) {
	cases := []struct {
		name      string
		target    EffectKind
		configure func(*DurableDependencies)
	}{
		{
			name:   "deploy_a_artifact",
			target: EffectDeployA,
			configure: func(deps *DurableDependencies) {
				deps.Artifacts = rejectingArtifactAuthenticator{ArtifactVerifier: deps.Artifacts}
			},
		},
		{
			name:   "deploy_b_artifact",
			target: EffectDeployB,
			configure: func(deps *DurableDependencies) {
				deps.Artifacts = rejectingArtifactAuthenticator{ArtifactVerifier: deps.Artifacts}
			},
		},
		{
			name:   "failure_notification",
			target: EffectNotifyFailure,
			configure: func(deps *DurableDependencies) {
				deps.Faults = rejectingFailureAuthenticator{FaultController: deps.Faults}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			a, b, script := validTwoFlowScript(t)
			store := &memorySessionStore{}
			effects := &durableEffects{script: script, creates: make(map[EffectKind]int), failKind: testCase.target}
			deps := durableTestDependencies(script, store, effects)
			seed := SessionSeed{ID: "wired-intent-authority-" + testCase.name, Events: [2]LandEvent{a, b}}
			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatal("setup target effect did not interrupt the session")
			}
			effects.mu.Lock()
			effects.failKind = ""
			effects.applyCalls = make(map[EffectKind]int)
			effects.mu.Unlock()
			testCase.configure(&deps)
			if _, err := RunSession(context.Background(), seed, deps); err == nil {
				t.Fatal("rejected prerequisite produced a successful proof")
			}
			effects.mu.Lock()
			defer effects.mu.Unlock()
			if effects.applyCalls[testCase.target] != 0 {
				t.Fatalf("target effect %s ran before prerequisite authentication", testCase.target)
			}
		})
	}
}

func TestPostFaultDeadlineTransitionsThroughCleanup(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: EffectNotifyFailure}
	clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	deps := durableTestDependencies(script, store, effects)
	setDurableTestClock(&deps, clock)
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
	setDurableTestClock(&deps, clock)
	seed := SessionSeed{ID: "acceptance-session-cleanup-deadline", Events: [2]LandEvent{a, b}}
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("cleanup failure did not interrupt the driver")
	}
	clock.Advance(6 * time.Minute)
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("expired cleanup produced a successful proof")
	}
	store.mu.Lock()
	deadlineState := *store.session
	store.mu.Unlock()
	if deadlineState.Phase != SessionCleanupFailed || deadlineState.Proof.Fault.ID == "" || deadlineState.TerminalError == "" {
		t.Fatalf("cleanup deadline state = %+v", deadlineState)
	}
	effects.mu.Lock()
	effects.failKind = ""
	effects.mu.Unlock()
	if _, err := RunSession(context.Background(), seed, deps); err == nil {
		t.Fatal("cleanup escalation incorrectly became a successful proof")
	}
	store.mu.Lock()
	resumedState := *store.session
	store.mu.Unlock()
	if resumedState.Phase != SessionFailed || !resumedState.Proof.Cleanup.NoResidue {
		t.Fatalf("resumed cleanup state = %+v", resumedState)
	}
}

func TestCleanupResponseLossAtDeadlineReconcilesNoResidue(t *testing.T) {
	a, b, script := validTwoFlowScript(t)
	store := &memorySessionStore{}
	effects := &durableEffects{script: script, creates: make(map[EffectKind]int), loseResponse: EffectRemoveFailure}
	clock := &manualClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	deps := durableTestDependencies(script, store, effects)
	setDurableTestClock(&deps, clock)
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
			setDurableTestClock(&deps, clock)
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
	clock := fixedClock{now: time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)}
	return DurableDependencies{
		Authority: testAuthority{}, Production: script, Artifacts: script, Health: script,
		Faults: script, Sessions: store, Effects: effects,
		Clock:         clock,
		StateSigner:   testSessionAuthenticator{secret: "test-only", clock: clock},
		StateVerifier: testSessionVerifier{auth: testSessionAuthenticator{secret: "test-only", clock: clock}},
	}
}

func setDurableTestClock(deps *DurableDependencies, clock Clock) {
	deps.Clock = clock
	deps.StateSigner = testSessionAuthenticator{secret: "test-only", clock: clock}
	deps.StateVerifier = testSessionVerifier{auth: testSessionAuthenticator{secret: "test-only", clock: clock}}
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
