package orchestrator

import (
	"testing"
	"time"
)

func SetApprovalPollIntervalForTest(t testing.TB, interval time.Duration) {
	t.Helper()
	previous := time.Duration(approvalPollIntervalNanos.Swap(int64(interval)))
	t.Cleanup(func() { approvalPollIntervalNanos.Store(int64(previous)) })
}
