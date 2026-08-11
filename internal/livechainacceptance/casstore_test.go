package livechainacceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	storagefs "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

type distributedTestStore struct{ *storagefs.ArtifactStore }

func (distributedTestStore) DistributedConditionalWrites() {}

type unsupportedDistributedStore struct{ distributedTestStore }

func (unsupportedDistributedStore) ConditionalWritesSupported(context.Context) (bool, error) {
	return false, nil
}

type recordingDistributedStore struct {
	distributedTestStore
	mu            sync.Mutex
	operations    []string
	failHeadMatch bool
}

func (store *recordingDistributedStore) PutIfAbsent(ctx context.Context, key string, reader io.Reader) (storage.ETag, error) {
	store.mu.Lock()
	store.operations = append(store.operations, "absent:"+key)
	store.mu.Unlock()
	return store.distributedTestStore.PutIfAbsent(ctx, key, reader)
}

func (store *recordingDistributedStore) PutIfMatch(ctx context.Context, key string, reader io.Reader, etag storage.ETag) (storage.ETag, error) {
	store.mu.Lock()
	store.operations = append(store.operations, "match:"+key)
	fail := store.failHeadMatch && strings.HasSuffix(key, "/head.json")
	store.mu.Unlock()
	if fail {
		return "", errors.New("injected head write failure")
	}
	return store.distributedTestStore.PutIfMatch(ctx, key, reader, etag)
}

func TestCASSessionStoreCreatesOneSealedGenesisAndHistory(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewCASSessionStore("acceptance", distributedTestStore{writer})
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "cas-genesis", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	var factoryCalls atomic.Int32
	factory := func(ctx context.Context) (Session, error) {
		factoryCalls.Add(1)
		return auth.InitialFactory(seed)(ctx)
	}
	const readers = 8
	results := make(chan Session, readers)
	errs := make(chan error, readers)
	var group sync.WaitGroup
	for range readers {
		group.Add(1)
		go func() {
			defer group.Done()
			got, loadErr := store.LoadOrCreate(context.Background(), seed, factory, verifier)
			results <- got
			errs <- loadErr
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for loadErr := range errs {
		if loadErr != nil {
			t.Fatal(loadErr)
		}
	}
	var first Session
	for got := range results {
		if first.ID == "" {
			first = got
		}
		if got.StateSeal.Digest != first.StateSeal.Digest || got.Version != 1 {
			t.Fatalf("genesis mismatch: %+v vs %+v", got.StateSeal, first.StateSeal)
		}
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("genesis factory calls = %d, want 1", factoryCalls.Load())
	}
	if ok, hasErr := writer.Has(context.Background(), store.historyKey(first)); hasErr != nil || !ok {
		t.Fatalf("genesis history present = %t, err = %v", ok, hasErr)
	}
}

func TestCASSessionStoreCoordinatesGenesisAcrossInstances(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	storeA, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "cross-instance-genesis", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	var factoryCalls atomic.Int32
	factory := func(ctx context.Context) (Session, error) {
		factoryCalls.Add(1)
		return auth.InitialFactory(seed)(ctx)
	}
	results := make(chan Session, 2)
	errs := make(chan error, 2)
	for _, store := range []*CASSessionStore{storeA, storeB} {
		go func(candidate *CASSessionStore) {
			got, loadErr := candidate.LoadOrCreate(context.Background(), seed, factory, verifier)
			results <- got
			errs <- loadErr
		}(store)
	}
	first := <-results
	second := <-results
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if first.StateSeal.Digest != second.StateSeal.Digest {
		t.Fatalf("cross-instance genesis digests = %s and %s", first.StateSeal.Digest, second.StateSeal.Digest)
	}
	keys, err := backend.List(context.Background(), storeA.sessionPrefix(seed.ID)+"/history/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("durable genesis authority records = %d, want 1 (factory calls %d)", len(keys), factoryCalls.Load())
	}
}

func TestCASSessionStoreAcceptsOnlyOneConcurrentSuccessor(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewCASSessionStore("acceptance", distributedTestStore{writer})
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "cas-successor", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	initial, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier)
	if err != nil {
		t.Fatal(err)
	}
	makeNext := func(message string) Session {
		next := initial
		next.Version++
		next.PreviousStateDigest = initial.StateSeal.Digest
		next.StateSeal = StateSeal{}
		next.Phase = SessionFailed
		next.TerminalError = message
		next.PhaseDeadline = time.Time{}
		sealed, sealErr := auth.SealSuccessor(context.Background(), initial, next)
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		return sealed
	}
	one := makeNext("one")
	two := makeNext("two")
	results := make(chan error, 2)
	for _, next := range []Session{one, two} {
		go func(candidate Session) {
			results <- store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, candidate, verifier)
		}(next)
	}
	successes := 0
	conflicts := 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrSessionConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}
}

func TestCASSessionStoreRejectsRolledBackHead(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	store, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "cas-rollback", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	initial, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier)
	if err != nil {
		t.Fatal(err)
	}
	next := initial
	next.Version++
	next.PreviousStateDigest = initial.StateSeal.Digest
	next.StateSeal = StateSeal{}
	next.Phase = SessionFailed
	next.TerminalError = "terminal"
	next.PhaseDeadline = time.Time{}
	next, err = auth.SealSuccessor(context.Background(), initial, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, verifier); err != nil {
		t.Fatal(err)
	}
	initialBytes, err := encodeStoredSession(initial)
	if err != nil {
		t.Fatal(err)
	}
	reader, etag, err := backend.GetWithETag(context.Background(), store.headKey(seed.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	if _, err := backend.PutIfMatch(context.Background(), store.headKey(seed.ID), bytes.NewReader(initialBytes), etag); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier); err == nil {
		t.Fatal("rolled-back signed head was accepted below the durable watermark")
	}
}

func TestCASSessionStoreRefusesNonDistributedConditionalWriter(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewCASSessionStore("acceptance", unsupportedDistributedStore{distributedTestStore{writer}})
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "unsupported-cas", Events: [2]LandEvent{a, b}}
	var factoryCalls atomic.Int32
	_, err = store.LoadOrCreate(context.Background(), seed, func(context.Context) (Session, error) {
		factoryCalls.Add(1)
		return Session{}, nil
	}, testSessionVerifier{})
	if err == nil || factoryCalls.Load() != 0 {
		t.Fatalf("unsupported store error/factory calls = %v/%d", err, factoryCalls.Load())
	}
}

func TestDecodeStoredSessionRejectsUnboundedAmbiguousInput(t *testing.T) {
	for name, body := range map[string][]byte{
		"oversized":     bytes.Repeat([]byte("x"), maxSessionStateBytes+1),
		"unknown_field": []byte(`{"ID":"x","unknown":true}`),
		"trailing":      []byte(`{} {}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeStoredSession(bytes.NewReader(body)); err == nil {
				t.Fatal("invalid durable session encoding was accepted")
			}
		})
	}
}

type rejectingSessionVerifier struct{}

func (rejectingSessionVerifier) Verify(context.Context, Session) error {
	return errors.New("rejected session state")
}

type digestOnlySessionVerifier struct{}

func (digestOnlySessionVerifier) Verify(_ context.Context, session Session) error {
	digest, err := digestSessionState(session)
	if err != nil {
		return err
	}
	if digest != session.StateSeal.Digest || session.StateSeal.Signature == "" {
		return errors.New("invalid session state")
	}
	return nil
}

func TestCASSessionStoreRejectsSameDigestDifferentGenesisBytes(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	stores := make([]*CASSessionStore, 2)
	for index := range stores {
		stores[index], err = NewCASSessionStore("acceptance", backend)
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "different-genesis-bytes", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	for index, store := range stores {
		go func(suffix string, candidate *CASSessionStore) {
			factory := func(ctx context.Context) (Session, error) {
				initial, factoryErr := auth.InitialFactory(seed)(ctx)
				if factoryErr != nil {
					return Session{}, factoryErr
				}
				initial.StateSeal.Signature += suffix
				arrived <- struct{}{}
				<-release
				return initial, nil
			}
			_, loadErr := candidate.LoadOrCreate(context.Background(), seed, factory, digestOnlySessionVerifier{})
			results <- loadErr
		}(fmt.Sprintf("-%d", index), store)
	}
	<-arrived
	<-arrived
	close(release)
	successes, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrSessionConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("different-byte genesis results = success %d conflict %d", successes, conflicts)
	}
}

func TestCASSessionStoreVerifiesLoadedHeadBeforeReturning(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	store, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "verify-loaded-head", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	if _, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), testSessionVerifier{auth: auth}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), rejectingSessionVerifier{}); err == nil {
		t.Fatal("store returned a head rejected by its verifier")
	}
}

func TestCASSessionStoreStagesCandidateBeforeHeadAndWatermarksOnlyWinner(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := &recordingDistributedStore{distributedTestStore: distributedTestStore{writer}, failHeadMatch: true}
	store, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "candidate-order", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	initial, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier)
	if err != nil {
		t.Fatal(err)
	}
	next := sealedFailedSuccessor(t, auth, initial, "ordered failure")
	backend.mu.Lock()
	backend.operations = nil
	backend.mu.Unlock()
	if err := store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, verifier); err == nil {
		t.Fatal("injected head failure was ignored")
	}
	backend.mu.Lock()
	operations := append([]string(nil), backend.operations...)
	backend.mu.Unlock()
	if len(operations) != 2 || !strings.Contains(operations[0], "/candidates/") || !strings.HasSuffix(operations[1], "/head.json") {
		t.Fatalf("candidate/head operations = %v", operations)
	}
	if ok, err := writer.Has(context.Background(), store.historyKey(next)); err != nil || ok {
		t.Fatalf("losing successor watermark present = %t, err = %v", ok, err)
	}
	if ok, err := writer.Has(context.Background(), store.candidateKey(next)); err != nil || !ok {
		t.Fatalf("staged candidate present = %t, err = %v", ok, err)
	}
}

func TestCASSessionStoreRejectsStalePredicatesBeforeStaging(t *testing.T) {
	for name, mutate := range map[string]func(*uint64, *string, *string){
		"version": func(version *uint64, _, _ *string) { *version++ },
		"digest":  func(_ *uint64, digest, _ *string) { *digest += "bad" },
		"seed":    func(_ *uint64, _, seed *string) { *seed += "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			writer, err := storagefs.NewArtifactStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			backend := distributedTestStore{writer}
			store, err := NewCASSessionStore("acceptance", backend)
			if err != nil {
				t.Fatal(err)
			}
			a, b, _ := validTwoFlowScript(t)
			sessionSeed := SessionSeed{ID: "stale-" + name, Events: [2]LandEvent{a, b}}
			auth := testSessionAuthenticator{secret: "test-only"}
			verifier := testSessionVerifier{auth: auth}
			initial, err := store.LoadOrCreate(context.Background(), sessionSeed, auth.InitialFactory(sessionSeed), verifier)
			if err != nil {
				t.Fatal(err)
			}
			next := sealedFailedSuccessor(t, auth, initial, "stale")
			version, digest, seed := initial.Version, initial.StateSeal.Digest, initial.SeedDigest
			mutate(&version, &digest, &seed)
			if err := store.CompareAndSwap(context.Background(), initial.ID, version, digest, seed, next, verifier); !errors.Is(err, ErrSessionConflict) {
				t.Fatalf("stale %s error = %v, want ErrSessionConflict", name, err)
			}
			if ok, err := writer.Has(context.Background(), store.candidateKey(next)); err != nil || ok {
				t.Fatalf("stale %s staged candidate = %t, err = %v", name, ok, err)
			}
		})
	}
}

type rejectSuccessorVerifier struct{ valid SessionVerifier }

func (verifier rejectSuccessorVerifier) Verify(ctx context.Context, session Session) error {
	if session.Version > 1 {
		return errors.New("successor rejected")
	}
	return verifier.valid.Verify(ctx, session)
}

func TestCASSessionStoreRejectsSuccessorBeforeStaging(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	store, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "reject-successor", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	valid := testSessionVerifier{auth: auth}
	initial, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), valid)
	if err != nil {
		t.Fatal(err)
	}
	next := sealedFailedSuccessor(t, auth, initial, "rejected")
	verifier := rejectSuccessorVerifier{valid: valid}
	if err := store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, verifier); err == nil {
		t.Fatal("rejected successor advanced the store")
	}
	if ok, err := writer.Has(context.Background(), store.candidateKey(next)); err != nil || ok {
		t.Fatalf("rejected successor candidate present = %t, err = %v", ok, err)
	}
}

func TestCASSessionStoreRejectsConflictingCandidateBytes(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	store, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "candidate-conflict", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	initial, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier)
	if err != nil {
		t.Fatal(err)
	}
	next := sealedFailedSuccessor(t, auth, initial, "candidate conflict")
	if _, err := backend.PutIfAbsent(context.Background(), store.candidateKey(next), strings.NewReader("conflict")); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, verifier); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("candidate conflict error = %v, want ErrSessionConflict", err)
	}
	loaded, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != initial.Version {
		t.Fatalf("candidate conflict advanced head to %d", loaded.Version)
	}
}

func TestCASSessionStoreRefusesConflictingWinnerWatermark(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend := distributedTestStore{writer}
	store, err := NewCASSessionStore("acceptance", backend)
	if err != nil {
		t.Fatal(err)
	}
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "watermark-conflict", Events: [2]LandEvent{a, b}}
	auth := testSessionAuthenticator{secret: "test-only"}
	verifier := testSessionVerifier{auth: auth}
	initial, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier)
	if err != nil {
		t.Fatal(err)
	}
	next := sealedFailedSuccessor(t, auth, initial, "watermark conflict")
	if _, err := backend.PutIfAbsent(context.Background(), store.historyKey(next), strings.NewReader("conflict")); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(context.Background(), initial.ID, initial.Version, initial.StateSeal.Digest, initial.SeedDigest, next, verifier); err == nil {
		t.Fatal("conflicting winner watermark was ignored")
	}
	if _, err := store.LoadOrCreate(context.Background(), seed, auth.InitialFactory(seed), verifier); err == nil {
		t.Fatal("conflicting authoritative watermark was ignored on reload")
	}
}

func sealedFailedSuccessor(t *testing.T, auth testSessionAuthenticator, initial Session, message string) Session {
	t.Helper()
	next := initial
	next.Version++
	next.PreviousStateDigest = initial.StateSeal.Digest
	next.StateSeal = StateSeal{}
	next.Phase = SessionFailed
	next.TerminalError = message
	next.PhaseDeadline = time.Time{}
	sealed, err := auth.SealSuccessor(context.Background(), initial, next)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

var _ storage.ConditionalWriter = distributedTestStore{}
