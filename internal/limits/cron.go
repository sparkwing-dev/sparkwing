package limits

import "time"

const (
	// ScheduleCatchUpMin bounds minimum enabled catch-up.
	ScheduleCatchUpMin time.Duration = 2 * time.Minute
	// ScheduleCatchUpMax bounds maximum catch-up.
	ScheduleCatchUpMax time.Duration = 24 * time.Hour
	// MaxSchedulesPerRepo bounds schedules per repository.
	MaxSchedulesPerRepo int = 20
	// MaxScheduledReposPerTeam bounds scheduled repositories per team.
	MaxScheduledReposPerTeam int = 10
	// RetainedScheduleFires bounds retained schedule fires.
	RetainedScheduleFires int = 200
)
