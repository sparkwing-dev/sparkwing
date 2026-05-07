package controller_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// TestClaim_TriggerPersistsThenClaims is the full trigger-queue
// contract: POST /triggers persists a pending row; POST
// /triggers/claim atomically flips it to 'claimed' and returns the
// full record; second claim finds nothing.
func TestClaim_TriggerPersistsThenClaims(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := httptest.NewServer(controller.New(st, nil).Handler())
	defer srv.Close()
	c := client.New(srv.URL, nil)

	// Fire a trigger via HTTP (not via the store directly -- we want
	// to verify the handler writes the row).
	resp := postJSON(t, srv.URL+"/api/v1/triggers", map[string]any{
		"pipeline": "claim-demo",
		"trigger":  map[string]string{"source": "test", "user": "alice"},
		"git":      map[string]string{"branch": "main", "sha": "abc"},
		"args":     map[string]string{"foo": "bar"},
	})
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 202 (body: %s)", resp.StatusCode, body)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)

	// Direct DB peek: row is there, status=pending.
	trig, err := st.GetTrigger(context.Background(), body.RunID)
	if err != nil {
		t.Fatalf("GetTrigger after POST: %v", err)
	}
	if trig.Status != "pending" {
		t.Errorf("status=%q want pending", trig.Status)
	}
	if trig.Pipeline != "claim-demo" {
		t.Errorf("pipeline=%q want claim-demo", trig.Pipeline)
	}
	if trig.TriggerSource != "test" || trig.TriggerUser != "alice" {
		t.Errorf("trigger metadata: %+v", trig)
	}
	if trig.GitBranch != "main" || trig.GitSHA != "abc" {
		t.Errorf("git metadata: %+v", trig)
	}
	if trig.Args["foo"] != "bar" {
		t.Errorf("args=%v want foo=bar", trig.Args)
	}

	// Claim via client. Must return the full row with status=claimed
	// and claimed_at populated.
	claimed, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger: %v", err)
	}
	if claimed == nil {
		t.Fatal("claimed=nil, expected a trigger")
	}
	if claimed.ID != body.RunID {
		t.Errorf("claimed.ID=%q want %q", claimed.ID, body.RunID)
	}
	if claimed.Status != "claimed" {
		t.Errorf("claimed.Status=%q want claimed", claimed.Status)
	}
	if claimed.ClaimedAt == nil {
		t.Error("claimed_at is nil")
	}

	// Second claim: queue empty, client returns (nil, nil).
	empty, err := c.ClaimTrigger(context.Background())
	if err != nil {
		t.Fatalf("ClaimTrigger empty: %v", err)
	}
	if empty != nil {
		t.Errorf("expected nil on empty queue, got %+v", empty)
	}
}

// TestClaim_FIFOOrdering verifies the oldest pending trigger is
// always claimed first. Workers depend on this for fairness.
func TestClaim_FIFOOrdering(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Three triggers with monotonically-increasing timestamps.
	for i, name := range []string{"first", "second", "third"} {
		if err := st.CreateTrigger(context.Background(), store.Trigger{
			ID:        "trig-" + name,
			Pipeline:  name,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatalf("CreateTrigger %s: %v", name, err)
		}
	}

	// Claim three; order must be first, second, third.
	for _, want := range []string{"first", "second", "third"} {
		got, err := st.ClaimNextTrigger(context.Background(), 0)
		if err != nil {
			t.Fatalf("ClaimNextTrigger for %s: %v", want, err)
		}
		if got.Pipeline != want {
			t.Errorf("claimed %q, want %q", got.Pipeline, want)
		}
	}

	// Fourth claim: empty.
	_, err = st.ClaimNextTrigger(context.Background(), 0)
	if err != store.ErrNotFound {
		t.Errorf("4th claim err=%v want ErrNotFound", err)
	}
}
