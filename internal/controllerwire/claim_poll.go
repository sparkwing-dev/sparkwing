package controllerwire

import "time"

// ClaimPollAfterHeader carries the whole number of seconds a controller
// suggests a claim loop wait before polling again. A runner may only widen
// its own cadence to it; a controller that enforces idle polling refuses an
// earlier poll with 429 and Retry-After.
const ClaimPollAfterHeader = "X-Sparkwing-Poll-After"

// MaxHonoredIdleClaimPoll caps the interval accepted by a runner.
const MaxHonoredIdleClaimPoll = 8 * time.Second
