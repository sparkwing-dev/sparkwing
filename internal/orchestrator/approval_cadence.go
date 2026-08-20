package orchestrator

import (
	"sync/atomic"
	"time"
)

const defaultApprovalPollInterval = 500 * time.Millisecond

var approvalPollIntervalNanos atomic.Int64

func approvalPollInterval() time.Duration {
	if interval := time.Duration(approvalPollIntervalNanos.Load()); interval > 0 {
		return interval
	}
	return defaultApprovalPollInterval
}
