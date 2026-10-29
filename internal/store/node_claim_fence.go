package store

const (

	// ClaimPollAfterHeader carries the whole number of seconds a controller
	// suggests a claim loop wait before polling again. A runner may only widen
	// its own cadence to it; a controller that enforces idle polling refuses an
	// earlier poll with 429 and Retry-After.
	ClaimPollAfterHeader = "X-Sparkwing-Poll-After"

	// RunnerIdentityHeader names the runner behind a claim or heartbeat: a
	// holder id, an executor name, or another label stable for one runner
	// process. Runners sharing one token carry distinct values, so a
	// controller can budget them apart instead of budgeting the token.
	RunnerIdentityHeader = "X-Sparkwing-Runner"
)
