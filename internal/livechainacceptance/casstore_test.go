package livechainacceptance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	storagefs "github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
)

func TestCASSessionStoreCreatesOneSealedGenesisAndHistory(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewCASSessionStore("acceptance", writer)
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

func TestCASSessionStoreAcceptsOnlyOneConcurrentSuccessor(t *testing.T) {
	writer, err := storagefs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewCASSessionStore("acceptance", writer)
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
