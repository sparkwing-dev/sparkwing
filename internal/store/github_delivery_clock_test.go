package store

import (
	"testing"
	"time"
)

func TestGitHubDeliveryLeaseUsesDatabaseClock(t *testing.T) {
	s := authorityStore(t)
	databaseNow := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s.SetTestClock(t, func() time.Time { return databaseNow })
	controllerNow := databaseNow.Add(-3 * time.Minute)
	ctx := t.Context()
	op := s.AsOperator()
	if err := op.EnqueueGitHubDelivery(ctx, "behind-clock", "push", []byte(`{}`), controllerNow); err != nil {
		t.Fatal(err)
	}
	delivery, err := op.ClaimGitHubDelivery(ctx)
	if err != nil || delivery == nil {
		t.Fatalf("claim: %+v, %v", delivery, err)
	}
	if err := s.githubDeliveryEffect(delivery.Context(ctx), "clock-test", func(*storeTx) error { return nil }); err != nil {
		t.Fatalf("new lease already expired with controller three minutes behind: %v", err)
	}
	if err := op.FinishGitHubDelivery(ctx, *delivery, ""); err != nil {
		t.Fatal(err)
	}
}
