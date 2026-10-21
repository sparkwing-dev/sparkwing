package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGitHubDeliveryCompletionReleasesPayload(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1_000_000, 0)
	op := s.AsOperator()
	if err := op.EnqueueGitHubDelivery(t.Context(), "delivery", "push", []byte(`{"ref":"main"}`), now); err != nil {
		t.Fatal(err)
	}
	claim, err := op.ClaimGitHubDelivery(t.Context(), now)
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, %v", claim, err)
	}
	if err := op.FinishGitHubDelivery(t.Context(), *claim, "", now); err != nil {
		t.Fatal(err)
	}
	var state string
	var bodyIsNull bool
	if err := s.db.QueryRowContext(t.Context(), `SELECT state,body IS NULL FROM github_app_deliveries WHERE delivery_id='delivery'`).Scan(&state, &bodyIsNull); err != nil {
		t.Fatal(err)
	}
	if state != "done" || !bodyIsNull {
		t.Fatalf("completed delivery retained payload: state=%q null=%v", state, bodyIsNull)
	}
}
