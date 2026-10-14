package limits

import "time"

const (
	// RunDeadline supplies the remote execution ceiling default; operators may change it, and local runs remain unbounded.
	RunDeadline time.Duration = 6 * time.Hour
	// LocalControllerIdleTimeout bounds idle local controller exit delay.
	LocalControllerIdleTimeout time.Duration = 5 * time.Minute
	// ClaimWait bounds default queue wait, separately bounded by a finite owner deadline.
	ClaimWait time.Duration = 24 * time.Hour
	// ApprovalWait leaves controller-owned and local approval waits indefinite by default.
	ApprovalWait time.Duration = 0
	// TotalWallTime preserves the absence of a total run wall-time cap.
	TotalWallTime time.Duration = 0
	// DebugPause bounds default debug pause.
	DebugPause time.Duration = 30 * time.Minute
	// ServiceReadyWait bounds default service readiness wait.
	ServiceReadyWait time.Duration = 30 * time.Second
	// SettleMargin bounds reserved time for outcome settlement.
	SettleMargin time.Duration = time.Minute
	// JobSlack bounds job and launch claim cleanup slack beyond attempt deadline.
	JobSlack time.Duration = 10 * time.Minute
)
