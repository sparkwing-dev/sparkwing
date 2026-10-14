package limits

import "time"

const (
	// WorkBillingMinimum bounds minimum billable work claim; plan/eval bill per second without this minimum.
	WorkBillingMinimum time.Duration = time.Minute
	// HostBillingMinimum preserves the orchestration minimum independently of job compute.
	HostBillingMinimum time.Duration = time.Minute
)
