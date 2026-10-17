package controllerwire

import "time"

// OverflowControl reports the authenticated account's monthly Cloud allowance.
type OverflowControl struct {
	MonthlyMinutes *int64    `json:"monthly_minutes"`
	UsedSeconds    int64     `json:"used_seconds"`
	PeriodStart    time.Time `json:"period_start"`
}

// OverflowControlWrite replaces the account's limit, with null selecting no cap.
type OverflowControlWrite struct {
	MonthlyMinutes *int64 `json:"monthly_minutes"`
}
