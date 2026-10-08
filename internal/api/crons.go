package api

// safety: one wire shape for a host's schedules and a controller's, flat and
// stringly typed -- RFC3339 times, empty when unset, nanosecond durations -- so
// the JSON does not move when the Go types behind it do.

// TimerStateView is the OS timer that calls the tick. A controller reports its
// own loop here instead, with [crons.ControllerTimerDetail] as the detail.
type TimerStateView struct {
	Installed bool   `json:"installed"`
	Foreign   bool   `json:"foreign"`
	Enabled   bool   `json:"enabled"`
	Stale     bool   `json:"stale"`
	Path      string `json:"path"`
	Binary    string `json:"binary"`
	Detail    string `json:"detail"`
}

// TickView is the last tick recorded against the store. At is empty until one
// has landed.
type TickView struct {
	At      string `json:"at"`
	Host    string `json:"host"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

// HealthView is whether the evaluator is running and what it holds.
type HealthView struct {
	Timer         TimerStateView `json:"timer"`
	LastTick      TickView       `json:"last_tick"`
	TickStale     bool           `json:"tick_stale"`
	Schedules     int            `json:"schedules"`
	Armed         int            `json:"armed"`
	Paused        int            `json:"paused"`
	Undeclared    int            `json:"undeclared"`
	Locked        int            `json:"locked"`
	Following     int            `json:"following"`
	Ahead         int            `json:"ahead"`
	MissingBinary int            `json:"missing_binary"`
	StaleOverride int            `json:"stale_override"`
	Detail        string         `json:"detail"`
	Remedy        string         `json:"remedy"`
}

// LockView is what a schedule runs. State is one of [crons.LockFollows],
// [crons.LockPinned], [crons.LockAhead], [crons.LockDirty] or [crons.LockMissing]; the other fields are
// empty while a schedule follows the checkout.
type LockView struct {
	Ref    string `json:"ref"`
	Binary string `json:"binary"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

// OverrideView names the declared fields the evaluating side has overridden,
// whether the declaration has moved under them since, and the values that were
// set. A field the override does not name is empty here, so a reader rebuilds
// the same override the evaluator holds and can print it beside the
// declaration.
type OverrideView struct {
	Fields    []string          `json:"fields"`
	Stale     bool              `json:"stale"`
	SetAt     string            `json:"set_at"`
	Cron      string            `json:"cron"`
	TZ        string            `json:"tz"`
	Overlap   string            `json:"overlap"`
	CatchUpNS int64             `json:"catch_up_ns"`
	Args      map[string]string `json:"args"`
}

// EffectiveView is the cadence a schedule actually runs.
type EffectiveView struct {
	Cron      string            `json:"cron"`
	TZ        string            `json:"tz"`
	Overlap   string            `json:"overlap"`
	CatchUpNS int64             `json:"catch_up_ns"`
	Args      map[string]string `json:"args"`
}

// ScheduleView is one schedule. Name is the display name; GitBranch is set
// only on a schedule pushed to a controller.
type ScheduleView struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ScheduleName string            `json:"schedule_name"`
	RepoPath     string            `json:"repo_path"`
	Pipeline     string            `json:"pipeline"`
	Where        string            `json:"where"`
	GitBranch    string            `json:"git_branch,omitempty"`
	Cron         string            `json:"cron"`
	TZ           string            `json:"tz"`
	Overlap      string            `json:"overlap"`
	CatchUpNS    int64             `json:"catch_up_ns"`
	Args         map[string]string `json:"args"`
	Lock         LockView          `json:"lock"`
	Override     OverrideView      `json:"override"`
	Effective    EffectiveView     `json:"effective"`
	Paused       bool              `json:"paused"`
	Declared     bool              `json:"declared"`
	State        string            `json:"state"`
	StateDetail  string            `json:"state_detail"`
	ArmedAt      string            `json:"armed_at"`
	UpdatedAt    string            `json:"updated_at"`
	LastFiredAt  *string           `json:"last_fired_at"`
	LastRunID    string            `json:"last_run_id"`
	LastOutcome  string            `json:"last_outcome"`
	NextDueAt    *string           `json:"next_due_at"`
}

// FireView is one resolved due instant. RunStatus is empty when the fire
// launched nothing or its run has since been pruned.
type FireView struct {
	ID         string            `json:"id"`
	ScheduleID string            `json:"schedule_id"`
	DueAt      string            `json:"due_at"`
	DecidedAt  string            `json:"decided_at"`
	Outcome    string            `json:"outcome"`
	RunID      string            `json:"run_id"`
	Detail     string            `json:"detail"`
	Args       map[string]string `json:"args"`
	RunStatus  string            `json:"run_status"`
}

// OverviewView is the evaluator's health and everything it holds.
type OverviewView struct {
	Health    HealthView     `json:"health"`
	Schedules []ScheduleView `json:"schedules"`
}

// DetailView is one schedule, its recent fires newest first, and the next
// instants it matches.
type DetailView struct {
	Schedule ScheduleView `json:"schedule"`
	Fires    []FireView   `json:"fires"`
	Upcoming []string     `json:"upcoming"`
}

// ScheduleEnvelope is what a write returns: the schedule as it now stands.
type ScheduleEnvelope struct {
	Schedule ScheduleView `json:"schedule"`
}

// RunEnvelope is what a launch returns: the run it started and the schedule as
// it now stands.
type RunEnvelope struct {
	RunID    string       `json:"run_id"`
	Schedule ScheduleView `json:"schedule"`
}
