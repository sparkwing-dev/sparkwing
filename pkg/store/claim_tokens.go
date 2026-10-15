package store

import "time"

// ClaimTokenPrefix marks the legacy host credential format.
const ClaimTokenPrefix = "swc"

// ClaimTokenKind names what a claim token's claim runs.
type ClaimTokenKind string

const (
	ClaimTokenPlan ClaimTokenKind = "plan"
	ClaimTokenWork ClaimTokenKind = "work"
	// ClaimTokenNode identifies a runner job claim.
	ClaimTokenNode ClaimTokenKind = "node"
	// ClaimTokenTrigger is a runner's claim on a trigger, which the secret
	// route alone answers.
	ClaimTokenTrigger ClaimTokenKind = "trigger"
)

// ClaimToken is the claim a presented token is bound to.
type ClaimToken struct {
	Prefix     string
	Team       Team
	RunID      string
	NodeID     string
	Generation int64
	Kind       ClaimTokenKind
	ExpiresAt  time.Time
}
