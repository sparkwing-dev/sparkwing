package store

import "time"

// MinIdleClaimPoll is the narrowest idle poll a controller ever suggests, so
// a claim loop polling faster is refused once the queue has been idle.
const MinIdleClaimPoll = 2 * time.Second

const (
	// RunnerIdentityHeader names the runner behind a claim or heartbeat: a
	// holder id, an executor name, or another label stable for one runner
	// process. Runners sharing one token carry distinct values, so a
	// controller can budget them apart instead of budgeting the token.
	RunnerIdentityHeader = "X-Sparkwing-Runner"
)
