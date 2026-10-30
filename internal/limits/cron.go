package limits

import "time"

const (
	// ScheduleCatchUpMin bounds minimum enabled catch-up.
	ScheduleCatchUpMin time.Duration = 2 * time.Minute
	// ScheduleCatchUpMax bounds maximum catch-up.
	ScheduleCatchUpMax time.Duration = 24 * time.Hour
	// MaxSchedulesPerTeam bounds declared schedules across all repositories.
	MaxSchedulesPerTeam int = 50
)
