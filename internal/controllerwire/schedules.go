package controllerwire

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// ScheduleArgs is a display projection. Schedule declarations are not classified by describe,
// so every value is private until an execution claim resolves it.
type ScheduleArgs struct{ values map[string]string }

// DisplayScheduleArgs retains names while hiding unclassified values.
func DisplayScheduleArgs(args map[string]string) ScheduleArgs {
	if len(args) == 0 {
		return ScheduleArgs{}
	}
	values := make(map[string]string, len(args))
	for name := range args {
		values[name] = runspec.RedactedArgValue
	}
	return ScheduleArgs{values: values}
}

// Values returns a copy of the display arguments.
func (args ScheduleArgs) Values() map[string]string { return maps.Clone(args.values) }

func (args ScheduleArgs) MarshalJSON() ([]byte, error) {
	if args.values == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(args.values)
}

func (args *ScheduleArgs) UnmarshalJSON(raw []byte) error {
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	*args = DisplayScheduleArgs(values)
	return nil
}

// TimerStateView is the OS timer that calls the tick. A controller reports its
// own loop here instead, with its loop status as the detail.
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

// LockView retains source identity and binary integrity separately from the
// observed pin state. Unpinned schedules leave the identity fields empty.
type LockView struct {
	Ref    string `json:"ref"`
	Binary string `json:"binary"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

// OverrideView displays the overridden fields beside their declaration.
// Args retain names only because schedule declarations have no secret classification.
type OverrideView struct {
	Fields    []string     `json:"fields"`
	Stale     bool         `json:"stale"`
	SetAt     string       `json:"set_at"`
	Cron      string       `json:"cron"`
	TZ        string       `json:"tz"`
	Overlap   string       `json:"overlap"`
	CatchUpNS int64        `json:"catch_up_ns"`
	Args      ScheduleArgs `json:"args"`
}

// EffectiveView is the cadence a schedule actually runs.
type EffectiveView struct {
	Cron      string       `json:"cron"`
	TZ        string       `json:"tz"`
	Overlap   string       `json:"overlap"`
	CatchUpNS int64        `json:"catch_up_ns"`
	Args      ScheduleArgs `json:"args"`
}

// ScheduleView is one schedule. Name is the display name; GitBranch is set
// only on a schedule pushed to a controller.
type ScheduleView struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	ScheduleName string        `json:"schedule_name"`
	RepoPath     string        `json:"repo_path"`
	Pipeline     string        `json:"pipeline"`
	Where        string        `json:"where"`
	GitBranch    string        `json:"git_branch,omitempty"`
	Cron         string        `json:"cron"`
	TZ           string        `json:"tz"`
	Overlap      string        `json:"overlap"`
	CatchUpNS    int64         `json:"catch_up_ns"`
	Args         ScheduleArgs  `json:"args"`
	Lock         LockView      `json:"lock"`
	Override     OverrideView  `json:"override"`
	Effective    EffectiveView `json:"effective"`
	Paused       bool          `json:"paused"`
	Declared     bool          `json:"declared"`
	State        string        `json:"state"`
	StateDetail  string        `json:"state_detail"`
	ArmedAt      string        `json:"armed_at"`
	UpdatedAt    string        `json:"updated_at"`
	LastFiredAt  *string       `json:"last_fired_at"`
	LastRunID    string        `json:"last_run_id"`
	LastOutcome  string        `json:"last_outcome"`
	NextDueAt    *string       `json:"next_due_at"`
}

// FireView is one resolved due instant. RunStatus is empty when the fire
// launched nothing or its run has since been pruned.
type FireView struct {
	ID         string       `json:"id"`
	ScheduleID string       `json:"schedule_id"`
	DueAt      string       `json:"due_at"`
	DecidedAt  string       `json:"decided_at"`
	Outcome    string       `json:"outcome"`
	RunID      string       `json:"run_id"`
	Detail     string       `json:"detail"`
	Args       ScheduleArgs `json:"args"`
	RunStatus  string       `json:"run_status"`
}

// ScheduleOverview is the evaluator's health and everything it holds.
type ScheduleOverview struct {
	Health    HealthView         `json:"health"`
	Schedules []ScheduleView     `json:"schedules"`
	Upcoming  []ScheduleUpcoming `json:"upcoming"`
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

// ScheduleUpcoming contains a controller-computed due instant. Clients do not
// recalculate cadence against their own clocks or time-zone data.
type ScheduleUpcoming struct {
	Schedule string `json:"schedule"`
	Name     string `json:"name"`
	At       string `json:"at"`
}

// ScheduleQuery selects undeclared rows and controller-computed upcoming instants.
type ScheduleQuery struct {
	All  bool
	Next int
}

// ScheduleReadQuery carries explicit history and upcoming counts. Zero fires requests no history.
type ScheduleReadQuery struct {
	Fires int
	Next  int
}

// ScheduleRun retains the launch identity and idempotency result without replacing the schedule ID.
type ScheduleRun struct {
	RunID    string       `json:"run_id"`
	Schedule ScheduleView `json:"schedule"`
	Replayed bool         `json:"replayed"`
}

// ScheduleDeletion distinguishes a removed schedule from an already absent one.
type ScheduleDeletion struct {
	ScheduleID string `json:"schedule_id"`
	Deleted    bool   `json:"deleted"`
}

// ScheduleManager uses the same state owner for api.sock and remote connections.
// The controller derives authority and commits cadence, history and launches.
type ScheduleManager interface {
	ListSchedules(context.Context, ScheduleQuery) (ScheduleOverview, error)
	ReadSchedule(context.Context, string, ScheduleReadQuery) (DetailView, error)
	SetSchedulePaused(context.Context, string, bool) (ScheduleView, error)
	OverrideSchedule(context.Context, string, cronspec.Override) (ScheduleView, error)
	ResetSchedule(context.Context, string) (ScheduleView, error)
	RunSchedule(context.Context, string, string) (ScheduleRun, error)
	DeleteSchedule(context.Context, string) (ScheduleDeletion, error)
}

// ScheduleWriteMode separates complete reconciliation from selected declaration updates.
type ScheduleWriteMode string

// Schedule write modes preserve unselected declarations during upserts.
const (
	ScheduleReplace ScheduleWriteMode = "replace"
	ScheduleUpsert  ScheduleWriteMode = "upsert"
)

// ScheduleRepoRequest proposes declarations within an explicit reconciliation scope.
type ScheduleRepoRequest struct {
	Mode      ScheduleWriteMode             `json:"mode"`
	RepoURL   string                        `json:"repo_url"`
	Branch    string                        `json:"branch,omitempty"`
	SHA       string                        `json:"sha,omitempty"`
	Follow    bool                          `json:"follow"`
	Schedules []runspec.ScheduleDeclaration `json:"schedules"`
}

// ScheduleRepoWritten returns the complete installed set and names withdrawn by replacement.
type ScheduleRepoWritten struct {
	Schedules []ScheduleView `json:"schedules"`
	Withdrawn []string       `json:"withdrawn"`
}

// ScheduleRepoDeletion confirms the repository and number of schedules removed.
type ScheduleRepoDeletion struct {
	RepoURL string `json:"repo_url"`
	Removed int    `json:"removed"`
}

// ScheduleRepositoryManager writes declarations atomically without withdrawing during upsert.
type ScheduleRepositoryManager interface {
	WriteSchedules(context.Context, ScheduleRepoRequest) (ScheduleRepoWritten, error)
	DeleteSchedulesForRepo(context.Context, string) (ScheduleRepoDeletion, error)
}

// LocalScheduleRegistration selects checkout declarations and host-captured inputs.
type LocalScheduleRegistration struct {
	Mode        ScheduleWriteMode             `json:"mode"`
	RepoPath    string                        `json:"repo_path"`
	Project     *projectconfig.Config         `json:"project"`
	Schedules   []runspec.ScheduleDeclaration `json:"schedules"`
	Environment []string                      `json:"environment"`
	Follow      bool                          `json:"follow"`
	Prove       bool                          `json:"prove"`
}

// LocalScheduleManager controls schedules on a verified local connection.
type LocalScheduleManager interface {
	RegisterLocalSchedules(context.Context, LocalScheduleRegistration) (ScheduleRepoWritten, error)
	SetLocalSchedulePinned(context.Context, string, bool) (ScheduleView, error)
	TickLocalSchedules(context.Context, bool) (TickView, error)
}
