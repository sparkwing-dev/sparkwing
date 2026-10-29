package limits

import "time"

const (
	// RunDeadline supplies the remote execution ceiling default; operators may change it, and local runs remain unbounded.
	RunDeadline time.Duration = 6 * time.Hour
	// LocalControllerIdleTimeout bounds idle local controller exit delay.
	LocalControllerIdleTimeout time.Duration = 5 * time.Minute
	// ClaimWait bounds default queue wait, separately bounded by a finite owner deadline.
	ClaimWait time.Duration = 24 * time.Hour
	// DebugPause bounds default debug pause.
	DebugPause time.Duration = 30 * time.Minute
	// jobSlack bounds job and launch claim cleanup slack beyond attempt deadline.
	jobSlack time.Duration = 10 * time.Minute
)
