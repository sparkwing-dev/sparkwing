package controllerwire

import "time"

// ClaimPollAfterHeader carries the whole number of seconds a controller
// suggests a claim loop wait before polling again. A runner may only widen
// its own cadence to it; a controller that enforces idle polling refuses an
// earlier poll with 429 and Retry-After.
const ClaimPollAfterHeader = "X-Sparkwing-Poll-After"

// MaxHonoredIdleClaimPoll caps the interval accepted by a runner.
const MaxHonoredIdleClaimPoll = 8 * time.Second

// ClaimWaitHeader carries the whole number of milliseconds a runner will let
// an empty claim wait for work. A controller that does not hold claims ignores it.
const ClaimWaitHeader = "X-Sparkwing-Claim-Wait"

// ClaimHeldHeader marks an empty claim answer that already waited for work,
// so the runner claims again at once instead of after its poll interval.
const ClaimHeldHeader = "X-Sparkwing-Claim-Held"

// MaxClaimWait caps how long a controller holds an empty claim. It stays
// under the local socket's 15 s read timeout and well inside the 1 min
// runner liveness horizon, because each claim records the runner as live.
const MaxClaimWait = 10 * time.Second
