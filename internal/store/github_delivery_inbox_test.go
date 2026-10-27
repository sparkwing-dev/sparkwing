package store_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
	claimed, err := op.ClaimGitHubDelivery(ctx)
	if err != nil || claimed == nil || claimed.ID != "first" || claimed.Event != "pull_request" || !bytes.Equal(claimed.Body, body) || !claimed.ReceivedAt.Equal(now) {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := op.FinishGitHubDelivery(ctx, *claimed, ""); err != nil {
		t.Fatal(err)
	}
	if next, err := op.ClaimGitHubDelivery(ctx); err != nil || next != nil {
		t.Fatalf("duplicate replay queued again: %+v, %v", next, err)
	}
}

func TestGitHubDeliveryInterruptedProcessingNeverRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	st, err := teststore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := teststore.Now(st)
	ctx := t.Context()
	if err := st.AsOperator().EnqueueGitHubDelivery(ctx, "delivery", "push", []byte("body"), now); err != nil {
		t.Fatal(err)
	}
	first, err := st.AsOperator().ClaimGitHubDelivery(ctx)
	if err != nil || first == nil {
		t.Fatalf("claim: %+v %v", first, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = teststore.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetTestClock(t, func() time.Time { return now.Add(3 * time.Minute) })
	if got, err := st.AsOperator().ClaimGitHubDelivery(ctx); err != nil || got != nil {
		t.Fatalf("automatically reclaimed: %+v %v", got, err)
	}
	if err := st.AsOperator().FinishGitHubDelivery(ctx, *first, ""); err == nil {
		t.Fatal("stale completion accepted")
	}
	var state, reason string
	if err := teststore.SQL(t, st).QueryRow(`SELECT state,last_error FROM github_app_deliveries`).Scan(&state, &reason); err != nil || state != "failed" || reason != "processing was interrupted" {
		t.Fatalf("expiry: %q %q %v", state, reason, err)
	}
}

func TestGitHubDeliveryFailuresNeverRetry(t *testing.T) {
	for _, reason := range []string{"temporary failure", "invalid configuration"} {
		t.Run(reason, func(t *testing.T) {
			st := storetest.Open(t)
			op := st.AsOperator()
			now := time.Now()
			ctx := t.Context()
			if err := op.EnqueueGitHubDelivery(ctx, "delivery", "push", []byte("body"), now); err != nil {
				t.Fatal(err)
			}
			claim, err := op.ClaimGitHubDelivery(ctx)
			if err != nil || claim == nil {
				t.Fatalf("claim: %+v %v", claim, err)
			}
			if err := op.FinishGitHubDelivery(ctx, *claim, reason); err != nil {
				t.Fatal(err)
			}
			if got, err := op.ClaimGitHubDelivery(ctx); err != nil || got != nil {
				t.Fatalf("automatically retried: %+v %v", got, err)
			}
			var state, gotReason string
			var body []byte
			if err := teststore.SQL(t, st).QueryRow(`SELECT state,last_error,body FROM github_app_deliveries`).Scan(&state, &gotReason, &body); err != nil || state != "failed" || gotReason != reason || string(body) != "body" {
				t.Fatalf("failure: %s %s %q %v", state, gotReason, body, err)
			}
		})
	}
}

func TestGitHubDeliveryLifecycleCrashDoesNotReplayAppliedSuspend(t *testing.T) {
	target := storetest.New(t)
	st := target.Open(t)
	ctx := t.Context()
	team := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := team.TestOnlySeedGitHubInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	op := st.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "suspend", "installation", []byte(`{"action":"suspend","installation":{"id":7}}`), now); err != nil {
		t.Fatal(err)
	}
	old, err := op.ClaimGitHubDelivery(ctx)
	if err != nil || old == nil {
		t.Fatalf("claim: %+v, %v", old, err)
	}
	if err := op.SetGitHubAppInstallationSuspended(old.Context(ctx), 7, true); err != nil {
		t.Fatal(err)
	}
	// Crash after the effect commits, before delivery completion.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st = target.Open(t)
	op = st.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "unsuspend", "installation", []byte(`{"action":"unsuspend","installation":{"id":7}}`), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	newer, err := op.ClaimGitHubDelivery(ctx)
	if err != nil || newer == nil || newer.ID != "unsuspend" {
		t.Fatalf("newer: %+v, %v", newer, err)
	}
	if err := op.SetGitHubAppInstallationSuspended(newer.Context(ctx), 7, false); err != nil {
		t.Fatal(err)
	}
	if err := op.FinishGitHubDelivery(ctx, *newer, ""); err != nil {
		t.Fatal(err)
	}
	st.SetTestClock(t, func() time.Time { return now.Add(3 * time.Minute) })
	if got, err := op.ClaimGitHubDelivery(ctx); err != nil || got != nil {
		t.Fatalf("automatic crash replay: %+v %v", got, err)
	}
	// An administrator's explicit replay preserves the committed effect receipt.
	if _, err := teststore.SQL(t, st).Exec(`UPDATE github_app_deliveries SET state='pending' WHERE delivery_id='suspend'`); err != nil {
		t.Fatal(err)
	}
	replay, err := op.ClaimGitHubDelivery(ctx)
	if err != nil || replay == nil || replay.ID != "suspend" {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	if err := op.SetGitHubAppInstallationSuspended(replay.Context(ctx), 7, true); err != nil {
		t.Fatal(err)
	}
	got, err := op.GitHubAppInstallationTeam(ctx, 7)
	if err != nil || got.Suspended {
		t.Fatalf("applied suspend overwrote newer unsuspend after restart: %+v, %v", got, err)
	}
}

func TestGitHubDeliveryFailedLifecycleDoesNotKeepReceipt(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	team := teamHandle(t, st, "acme")
	now := time.Now()
	if _, err := team.TestOnlySeedGitHubInstallation(ctx, acmeInstallation(), now); err != nil {
		t.Fatal(err)
	}
	op := st.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "suspend", "installation", []byte(`{"action":"suspend","installation":{"id":7}}`), now); err != nil {
		t.Fatal(err)
	}
	claim, err := op.ClaimGitHubDelivery(ctx)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v, %v", claim, err)
	}
	db := teststore.SQL(t, st)
	if _, err := db.Exec(`ALTER TABLE cron_schedules RENAME TO unavailable_cron_schedules`); err != nil {
		t.Fatal(err)
	}
	if err := op.SetGitHubAppInstallationSuspended(claim.Context(ctx), 7, true); err == nil {
		t.Fatal("suspension committed without schedule withdrawal")
	}
	got, err := op.GitHubAppInstallationTeam(ctx, 7)
	if err != nil || got.Suspended {
		t.Fatalf("failed suspension leaked: %+v, %v", got, err)
	}
	if _, err := db.Exec(`ALTER TABLE unavailable_cron_schedules RENAME TO cron_schedules`); err != nil {
		t.Fatal(err)
	}
	if err := op.SetGitHubAppInstallationSuspended(claim.Context(ctx), 7, true); err != nil {
		t.Fatal(err)
	}
	got, err = op.GitHubAppInstallationTeam(ctx, 7)
	if err != nil || !got.Suspended {
		t.Fatalf("failed effect kept receipt and prevented retry: %+v, %v", got, err)
	}
}
