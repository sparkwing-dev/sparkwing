package limits

import "time"

const (
	// WorkBillingMinimum bounds minimum billable work claim; plan/eval bill per second without this minimum.
	WorkBillingMinimum time.Duration = time.Minute
)
