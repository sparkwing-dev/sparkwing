// Package crons drives the local cron scheduler: it turns the schedules a
// repository declares into armed rows in this home's runs store, and turns one
// per-minute tick into runs.
//
// The package is pure orchestration. Every clock reading arrives through
// [Service.Now], every launch through a [Launcher] the caller supplies, and
// every durable fact through [store.Store], so the same code serves the CLI,
// the dashboard, and a test with a fake clock and a fake launcher.
//
// A schedule is identified by [ScheduleID], derived from the repository path
// and the pipeline name, so re-arming the same checkout finds the same row and
// two checkouts of one repository stay separate schedules.
package crons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ScheduleIDPrefix marks a schedule id.
const ScheduleIDPrefix = "crn_"

// scheduleIDHexLen is how much of the digest a schedule id carries. Twelve hex
// characters keep the id readable in a table while leaving collisions far
// beyond the number of checkouts one host arms.
const scheduleIDHexLen = 12

// ScheduleID is the stable identity of one pipeline's schedule in one
// checkout: crn_ followed by the first twelve hex characters of
// sha256(repoPath + NUL + pipeline).
func ScheduleID(repoPath, pipeline string) string {
	sum := sha256.Sum256([]byte(repoPath + "\x00" + pipeline))
	return ScheduleIDPrefix + hex.EncodeToString(sum[:])[:scheduleIDHexLen]
}

// DisplayName is the name an operator types and reads: the repository
// directory's base name, a slash, and the pipeline name.
func DisplayName(s store.CronSchedule) string {
	return filepath.Base(s.RepoPath) + "/" + s.Pipeline
}

// Declared is one schedule a repository's sparkwing.yaml declares.
type Declared struct {
	RepoPath string
	Pipeline string
	Trigger  *pipelines.ScheduleTrigger
}

// DeclaredSchedules reads <repoRoot>/.sparkwing/sparkwing.yaml and returns the
// pipelines that declare a schedule trigger, in declaration order. A repository
// with no config file, or one that declares no cadence, yields no schedules and
// no error; the config's own validation rejects a malformed cron before it gets
// here.
func DeclaredSchedules(repoRoot string) ([]Declared, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	cfg, err := projectconfig.Load(filepath.Join(root, ".sparkwing", projectconfig.Filename))
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}
	var out []Declared
	for _, p := range cfg.Pipelines {
		if p.On.Schedule == nil {
			continue
		}
		out = append(out, Declared{RepoPath: root, Pipeline: p.Name, Trigger: p.On.Schedule})
	}
	return out, nil
}

// Launcher is how a due schedule becomes a run.
type Launcher interface {
	// Launch starts the schedule's pipeline for the given due instant and
	// returns the run id.
	Launch(ctx context.Context, s store.CronSchedule, due time.Time) (runID string, err error)

	// Active reports whether the run is still pending or running, after
	// reconciling runs whose executor died, so a crashed run never suppresses
	// a schedule forever.
	Active(ctx context.Context, runID string) (bool, error)
}

// Service evaluates schedules against one home's runs store.
//
// LockPath must be set before [Service.Tick] is called: the tick takes an
// exclusive lock on it so two timers, or a timer and a hand-run tick, cannot
// resolve the same due instant twice.
type Service struct {
	Store    *store.Store
	Launcher Launcher

	// Now reads the clock. Nil means [time.Now].
	Now func() time.Time

	// Host and Version are recorded on each tick, so an operator can tell
	// which machine and which build last evaluated this home.
	Host    string
	Version string

	// LockPath is the file the tick locks, conventionally <home>/crons.lock.
	LockPath string

	// ArmedBy is recorded when a schedule is first armed, such as user@host.
	ArmedBy string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// State names what a stored schedule does at the next due instant.
const (
	StateArmed      = "armed"
	StatePaused     = "paused"
	StateUndeclared = "undeclared"
)

// Row is a stored schedule plus the fields a renderer needs and the store does
// not keep: the display name, the state, and the resolved zone.
type Row struct {
	store.CronSchedule
	Name     string         `json:"name"`
	State    string         `json:"state"`
	Location *time.Location `json:"-"`
}

func newRow(s store.CronSchedule) Row {
	loc, err := trigger(s).Location()
	if err != nil {
		loc = time.UTC
	}
	return Row{CronSchedule: s, Name: DisplayName(s), State: stateOf(s), Location: loc}
}

func stateOf(s store.CronSchedule) string {
	switch {
	case !s.Declared:
		return StateUndeclared
	case s.Paused:
		return StatePaused
	default:
		return StateArmed
	}
}

// trigger rebuilds the declaration a stored row came from, so the zone,
// overlap and catch-up defaults are resolved by the same code the config
// validator uses.
func trigger(s store.CronSchedule) *pipelines.ScheduleTrigger {
	return &pipelines.ScheduleTrigger{Cron: s.Cron, TZ: s.TZ, Overlap: s.Overlap}
}

// evaluable is a stored schedule with its expression parsed and its zone
// resolved, which is everything the evaluator needs.
type evaluable struct {
	schedule *cronspec.Schedule
	loc      *time.Location
	catchUp  time.Duration
}

func prepare(s store.CronSchedule) (evaluable, error) {
	parsed, err := cronspec.Parse(s.Cron)
	if err != nil {
		return evaluable{}, err
	}
	loc, err := trigger(s).Location()
	if err != nil {
		return evaluable{}, err
	}
	catchUp := s.CatchUp
	if catchUp <= 0 {
		catchUp = pipelines.DefaultScheduleCatchUp
	}
	return evaluable{schedule: parsed, loc: loc, catchUp: catchUp}, nil
}

func (e evaluable) nextAfter(at time.Time) *time.Time {
	next := e.schedule.Next(at, e.loc)
	if next.IsZero() {
		return nil
	}
	return &next
}

// List returns every schedule this home knows, ordered by repository path then
// pipeline, including the ones the repository no longer declares.
func (s *Service) List(ctx context.Context) ([]Row, error) {
	stored, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(stored))
	for _, sched := range stored {
		out = append(out, newRow(sched))
	}
	return out, nil
}

// Show returns one schedule and its most recent resolved instants, newest
// first. A fires count of zero or less reads the store's default page.
func (s *Service) Show(ctx context.Context, id string, fires int) (Row, []store.CronFire, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return Row{}, nil, err
	}
	history, err := s.Store.ListCronFires(ctx, id, fires)
	if err != nil {
		return Row{}, nil, err
	}
	return newRow(sched), history, nil
}

// Pause stops a schedule firing. Its cursor still advances on each tick, so
// resuming does not replay the instants that passed while it was paused.
func (s *Service) Pause(ctx context.Context, id string) error {
	return s.Store.SetCronSchedulePaused(ctx, id, true, s.now())
}

// Resume lets a paused schedule fire again from its next due instant.
func (s *Service) Resume(ctx context.Context, id string) error {
	return s.Store.SetCronSchedulePaused(ctx, id, false, s.now())
}

// Upcoming returns the next n instants a schedule matches after now, in the
// zone it is read in. It returns fewer than n when the expression stops
// matching within the evaluator's horizon.
func (s *Service) Upcoming(ctx context.Context, id string, n int) ([]time.Time, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return nil, err
	}
	eval, err := prepare(sched)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", DisplayName(sched), err)
	}
	return eval.schedule.Upcoming(s.now(), eval.loc, n), nil
}

// RunNow launches a schedule's pipeline immediately, whatever its cadence says
// and whether or not it is paused, and records the launch in its history. The
// cursor does not move: a manual run is not one of the cadence's due instants.
func (s *Service) RunNow(ctx context.Context, id string) (string, error) {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return "", err
	}
	now := s.now()
	runID, err := s.Launcher.Launch(ctx, sched, now)
	if err != nil {
		return "", fmt.Errorf("%s: %w", DisplayName(sched), err)
	}
	var next *time.Time
	if eval, perr := prepare(sched); perr == nil {
		next = eval.nextAfter(now)
	} else {
		next = sched.NextDueAt
	}
	fire := &store.CronFire{
		DueAt:     now,
		DecidedAt: now,
		Outcome:   store.CronOutcomeFired,
		RunID:     runID,
		Detail:    "run now",
	}
	if err := s.Store.ResolveCronDue(ctx, id, sched.CursorAt, next, fire, now); err != nil {
		return runID, fmt.Errorf("run %s launched but its fire could not be recorded: %w", runID, err)
	}
	return runID, nil
}

// ErrAmbiguousName is returned, wrapped, when a bare pipeline name matches
// more than one armed schedule.
var ErrAmbiguousName = errors.New("more than one schedule carries that pipeline name")

// Resolve finds one schedule from what an operator typed: a schedule id, a
// repo/pipeline display name, or a bare pipeline name that is unique across
// this home's schedules. An ambiguous bare name is an error naming every
// candidate as repo/pipeline.
func (s *Service) Resolve(ctx context.Context, name string) (store.CronSchedule, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return store.CronSchedule{}, errors.New("crons: a schedule name is required")
	}
	if strings.HasPrefix(trimmed, ScheduleIDPrefix) {
		return s.Store.GetCronSchedule(ctx, trimmed)
	}
	stored, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return store.CronSchedule{}, err
	}
	var byName, byPipeline []store.CronSchedule
	for _, sched := range stored {
		switch {
		case DisplayName(sched) == trimmed:
			byName = append(byName, sched)
		case sched.Pipeline == trimmed:
			byPipeline = append(byPipeline, sched)
		}
	}
	candidates := byName
	if len(candidates) == 0 {
		candidates = byPipeline
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return store.CronSchedule{}, fmt.Errorf("crons: no schedule named %q is armed here; `sparkwing crons list` names them", trimmed)
	}
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, DisplayName(c))
	}
	sort.Strings(names)
	return store.CronSchedule{}, fmt.Errorf("crons: %q: %w: %s",
		trimmed, ErrAmbiguousName, strings.Join(names, ", "))
}
