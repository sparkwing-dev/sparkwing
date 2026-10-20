package store

const (

	// ClaimPollAfterHeader carries the whole number of seconds a controller
	// suggests a claim loop wait before polling again. It is advice a runner
	// may only widen its own cadence to, so a runner that ignores it polls
	// exactly as often as it was configured to.
	ClaimPollAfterHeader = "X-Sparkwing-Poll-After"

	// RunnerIdentityHeader names the runner behind a claim or heartbeat: a
	// holder id, an executor name, or another label stable for one runner
	// process. Runners sharing one token carry distinct values, so a
	// controller can budget them apart instead of budgeting the token.
	RunnerIdentityHeader = "X-Sparkwing-Runner"
)
