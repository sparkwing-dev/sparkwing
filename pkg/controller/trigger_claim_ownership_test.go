package controller_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type triggerOwnershipFixture struct {
	url      string
	holder   string
	stranger string
	store    *store.Store
}

func newTriggerOwnershipFixture(t *testing.T) triggerOwnershipFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	holder, _, err := st.CreateToken("worker-a", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken holder: %v", err)
	}
	stranger, _, err := st.CreateToken("worker-b", store.TokenKindRunner,
		[]string{controller.ScopeTriggersClaim}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	seedRepoTrigger(t, st, "run-owned", "acme/web")

	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)

	trig, err := client.NewWithToken(srv.URL, nil, holder).ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("holder ClaimTrigger: %v", err)
	}
	if trig == nil || trig.ID != "run-owned" {
		t.Fatalf("holder claimed %+v, want run-owned", trig)
	}
	return triggerOwnershipFixture{url: srv.URL, holder: holder, stranger: stranger, store: st}
}

func (f triggerOwnershipFixture) post(t *testing.T, token, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.url+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func (f triggerOwnershipFixture) status(t *testing.T) string {
	t.Helper()
	trig, err := f.store.GetTrigger(context.Background(), "run-owned")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	return trig.Status
}

func TestTriggerClaim_StrangerCannotFinishOrHeartbeat(t *testing.T) {
	f := newTriggerOwnershipFixture(t)

	if got := f.post(t, f.stranger, "/api/v1/triggers/run-owned/done"); got != http.StatusForbidden {
		t.Errorf("stranger done status=%d want 403", got)
	}
	if got := f.status(t); got != "claimed" {
		t.Errorf("trigger status=%q after the stranger's done, want claimed", got)
	}
	if got := f.post(t, f.stranger, "/api/v1/triggers/run-owned/heartbeat"); got != http.StatusForbidden {
		t.Errorf("stranger heartbeat status=%d want 403", got)
	}
}

func TestTriggerClaim_HolderFinishesAndRetries(t *testing.T) {
	f := newTriggerOwnershipFixture(t)

	if got := f.post(t, f.holder, "/api/v1/triggers/run-owned/heartbeat"); got != http.StatusOK {
		t.Fatalf("holder heartbeat status=%d want 200", got)
	}
	if got := f.post(t, f.holder, "/api/v1/triggers/run-owned/done"); got != http.StatusNoContent {
		t.Fatalf("holder done status=%d want 204", got)
	}
	if got := f.status(t); got != "done" {
		t.Fatalf("trigger status=%q after the holder's done, want done", got)
	}
	// safety: a finished trigger keeps its claimant, so the client's own retry
	// of a done whose response was lost still lands.
	if got := f.post(t, f.holder, "/api/v1/triggers/run-owned/done"); got != http.StatusNoContent {
		t.Errorf("holder done retry status=%d want 204", got)
	}
}

// A trigger nobody holds reads as not-found rather than forbidden: a
// worker's heartbeat loop stops on not-found, and answering forbidden
// would keep it retrying until its silence window kills the consumer.
func TestTriggerClaim_UnclaimedTriggerAnswersNotFound(t *testing.T) {
	f := newTriggerOwnershipFixture(t)
	seedRepoTrigger(t, f.store, "run-pending", "acme/web")

	if got := f.post(t, f.holder, "/api/v1/triggers/run-pending/done"); got != http.StatusNotFound {
		t.Errorf("done on an unclaimed trigger status=%d want 404", got)
	}
	if got := f.post(t, f.holder, "/api/v1/triggers/run-missing/heartbeat"); got != http.StatusNotFound {
		t.Errorf("heartbeat on a trigger that does not exist status=%d want 404", got)
	}
	trig, err := f.store.GetTrigger(context.Background(), "run-pending")
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trig.Status != "pending" {
		t.Errorf("unclaimed trigger status=%q, want pending", trig.Status)
	}
}

// The reaped-claim contract the in-tree heartbeat loops key on: once the
// claim is requeued, the former holder's own heartbeat is not-found, and
// the client surfaces that as store.ErrNotFound so the loop stops the
// child instead of retrying until its silence window terminates it.
func TestTriggerClaim_RequeuedClaimAnswersNotFoundToItsFormerHolder(t *testing.T) {
	f := newTriggerOwnershipFixture(t)
	ctx := context.Background()

	requeued, err := f.store.RequeueUnstartedClaim(ctx, "run-owned")
	if err != nil || !requeued {
		t.Fatalf("RequeueUnstartedClaim = (%v, %v), want (true, nil)", requeued, err)
	}

	if got := f.post(t, f.holder, "/api/v1/triggers/run-owned/heartbeat"); got != http.StatusNotFound {
		t.Errorf("former holder heartbeat status=%d want 404", got)
	}
	if _, err := client.NewWithToken(f.url, nil, f.holder).
		HeartbeatTrigger(ctx, "run-owned"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("client HeartbeatTrigger err=%v, want store.ErrNotFound", err)
	}
}
