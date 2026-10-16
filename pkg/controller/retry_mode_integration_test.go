//go:build rewrite_integration

package controller

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestReviewRetryModeIsPartOfCommittedReplay(t *testing.T) {
	s, options, _, _ := retryActionFixture(t, "private-value", true)
	control, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.Retry(t.Context(), "run", controllerwire.RetryRequest{Mode: controllerwire.RetryFailed, IdempotencyKey: "retry"}); err != nil {
		t.Fatal(err)
	}
	result, err := control.Retry(t.Context(), "1", controllerwire.RetryRequest{Mode: controllerwire.RetryAll, IdempotencyKey: "retry"})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.Conflict {
		t.Fatalf("changing retry mode reused a committed selection: run=%q replayed=%t error=%v", result.RunID, result.Replayed, err)
	}
	var count int
	if err := s.store.DB().QueryRow(`SELECT COUNT(*) FROM runs WHERE team='a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("conflicting selection wrote another execution: %d %v", count, err)
	}
}
