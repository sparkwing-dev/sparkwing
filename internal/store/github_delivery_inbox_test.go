package store_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestGitHubDeliveryReplayAndEventBinding(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	op := st.AsOperator()
	ctx := t.Context()
	now := time.Unix(1_000_000, 0)
	body := []byte(`{"action":"opened"}`)
	if err := op.EnqueueGitHubDelivery(ctx, "first", "pull_request", body, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := op.EnqueueGitHubDelivery(ctx, id, "pull_request", body, now); err != nil {
			t.Fatalf("identical replay %q: %v", id, err)
		}
	}
	if err := op.EnqueueGitHubDelivery(ctx, "first", "pull_request", []byte("different"), now); !errors.Is(err, store.ErrGitHubDeliveryConflict) {
		t.Fatalf("delivery ID rebound to a body: %v", err)
	}
	if err := op.EnqueueGitHubDelivery(ctx, "second", "pull_request", []byte("different"), now); !errors.Is(err, store.ErrGitHubDeliveryConflict) {
		t.Fatalf("replayed delivery ID rebound to a body: %v", err)
	}
	if err := op.EnqueueGitHubDelivery(ctx, "third", "push", body, now); !errors.Is(err, store.ErrGitHubDeliveryConflict) {
		t.Fatalf("digest rebound to an event: %v", err)
	}
	claimed, err := op.ClaimGitHubDelivery(ctx, now)
	if err != nil || claimed == nil || claimed.ID != "first" || claimed.Event != "pull_request" || !bytes.Equal(claimed.Body, body) || claimed.Attempts != 1 || !claimed.ReceivedAt.Equal(now) {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := op.FinishGitHubDelivery(ctx, *claimed, "", now); err != nil {
		t.Fatal(err)
	}
	if next, err := op.ClaimGitHubDelivery(ctx, now); err != nil || next != nil {
		t.Fatalf("duplicate replay queued again: %+v, %v", next, err)
	}
}

func TestGitHubDeliveryReopenRetryAndStaleLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	now := time.Unix(1_000_000, 0)
	if err := st.AsOperator().EnqueueGitHubDelivery(ctx, "delivery", "push", []byte("body"), now); err != nil {
		t.Fatal(err)
	}
	first, err := st.AsOperator().ClaimGitHubDelivery(ctx, now)
	if err != nil || first == nil {
		t.Fatalf("first claim: %+v, %v", first, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	op := st.AsOperator()
	if got, err := op.ClaimGitHubDelivery(ctx, now.Add(time.Minute)); err != nil || got != nil {
		t.Fatalf("unexpired lease: %+v, %v", got, err)
	}
	second, err := op.ClaimGitHubDelivery(ctx, now.Add(2*time.Minute))
	if err != nil || second == nil || second.Lease == first.Lease || second.Attempts != 2 || !bytes.Equal(second.Body, []byte("body")) {
		t.Fatalf("reclaimed after restart: %+v, %v", second, err)
	}
	if err := op.FinishGitHubDelivery(ctx, *first, "", now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale completion accepted")
	}
	if err := op.FinishGitHubDelivery(ctx, *second, "temporary failure", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, err := op.ClaimGitHubDelivery(ctx, now.Add(2*time.Minute)); err != nil || got != nil {
		t.Fatalf("backoff ignored: %+v, %v", got, err)
	}
	third, err := op.ClaimGitHubDelivery(ctx, now.Add(2*time.Minute+2*time.Second))
	if err != nil || third == nil || third.Attempts != 3 || third.LastError != "temporary failure" {
		t.Fatalf("retry vanished: %+v, %v", third, err)
	}
	if err := op.FinishGitHubDelivery(ctx, *third, "", now.Add(2*time.Minute+2*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubDeliveryRetriesRemainClaimable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	op := st.AsOperator()
	ctx := t.Context()
	now := time.Unix(1_000_000, 0)
	if err := op.EnqueueGitHubDelivery(ctx, "delivery", "push", []byte("body"), now); err != nil {
		t.Fatal(err)
	}
	for attempt := int64(1); attempt <= 16; attempt++ {
		at := now.Add(time.Duration(attempt) * time.Hour)
		claim, err := op.ClaimGitHubDelivery(ctx, at)
		if err != nil || claim == nil || claim.Attempts != attempt || !bytes.Equal(claim.Body, []byte("body")) {
			t.Fatalf("attempt %d: %+v, %v", attempt, claim, err)
		}
		if err := op.FinishGitHubDelivery(ctx, *claim, "temporary failure", at); err != nil {
			t.Fatal(err)
		}
	}
}
