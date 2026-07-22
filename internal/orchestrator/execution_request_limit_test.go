package orchestrator

import (
	"context"
	"testing"
	"time"
)

func TestExecutionRequestLimiter_BoundsOutstandingDaemonWaiters(t *testing.T) {
	ctx := withLocalAdmission(context.Background(), &LocalAdmission{}, "", "pipeline", nil, 1)
	first, err := acquireExecutionRequestPermit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := acquireExecutionRequestPermit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second()

	waitCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if _, err := acquireExecutionRequestPermit(waitCtx); err == nil {
		t.Fatal("third execution request acquired beyond 2 * MaxParallel")
	}
}
