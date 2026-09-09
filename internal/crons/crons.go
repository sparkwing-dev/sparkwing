// Package crons drives the local cron scheduler: it turns the schedules a
// repository declares into armed rows in this home's runs store, and turns one
// per-minute tick into runs.
//
// The package is pure orchestration. Every clock reading arrives through
// [Service.Now], every launch through a [Launcher] the caller supplies, and
// every durable fact through [store.Store], so the same code serves the CLI,
// the dashboard, and a test with a fake clock and a fake launcher.
//
// A schedule is identified by [ScheduleID], derived from the repository path,
// the pipeline name and the entry's name, so re-arming the same checkout finds
// the same row and two checkouts of one repository stay separate schedules.
package crons

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ScheduleIDPrefix marks a schedule id.
const ScheduleIDPrefix = "crn_"

// safety: twelve hex characters stay readable in a table and leave collisions far beyond one host's checkouts.
const scheduleIDHexLen = 12

// ScheduleID is the stable identity of one of a pipeline's schedules in one
// checkout: crn_ followed by the first twelve hex characters of
// sha256(repoPath + NUL + pipeline) for the entry named "default", and of
// sha256(repoPath + NUL + pipeline + NUL + name) for any other. The lone
// schedule of a pipeline keeps the id it had before entries carried names.
func ScheduleID(repoPath, pipeline, name string) string {
	seed := repoPath + "\x00" + pipeline
	if name != "" && name != store.CronScheduleDefaultName {
		seed += "\x00" + name
	}
	sum := sha256.Sum256([]byte(seed))
	return ScheduleIDPrefix + hex.EncodeToString(sum[:])[:scheduleIDHexLen]
}

// DisplayName is the name an operator types and reads: the repository
// directory's base name, a slash, and the pipeline name, followed by a slash
// and the entry's name for every entry but "default".
func DisplayName(s store.CronSchedule) string {
	return displayName(s.RepoPath, s.Pipeline, s.Name)
}

func displayName(repoPath, pipeline, name string) string {
	base := repoLabel(repoPath) + "/" + pipeline
	if name == "" || name == store.CronScheduleDefaultName {
		return base
	}
	return base + "/" + name
}

// safety: a controller row's repo_path is a clone URL, whose base name alone
// ("sparkwing") collides across owners, so the owner rides in the label. The
// scp form git@host:owner/name spells the owner after a colon rather than a
// slash, so the label reads the same for both spellings of one repository.
func repoLabel(repoPath string) string {
	if !PushedRepo(repoPath) {
		return filepath.Base(repoPath)
	}
	trimmed := strings.TrimSuffix(strings.TrimRight(repoPath, "/"), ".git")
	if colon := strings.LastIndex(trimmed, ":"); colon >= 0 {
		if rest := trimmed[colon+1:]; !strings.HasPrefix(rest, "//") && strings.Contains(rest, "/") {
			trimmed = rest
		}
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) < 2 {
		return trimmed
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// PushedRepo reports whether a stored repo_path names a repository pushed to a
// controller rather than a checkout on this machine. Every clone URL a push is
// allowed to carry counts, including the scp form under any user name, not
// only git@.
func PushedRepo(repoPath string) bool {
	for _, scheme := range []string{"https://", "http://", "ssh://", "git://"} {
		if strings.HasPrefix(repoPath, scheme) {
			return true
		}
	}
	// safety: the scp form carries no scheme, so only the clone validator tells
	// user@host:path from a directory on this machine.
	_, err := sourceurl.ValidateCloneURL(repoPath)
	return err == nil
}

// Pushed reports whether a schedule was pushed to a controller, so its
// declaration is what the push carried and no working tree answers for it.
func Pushed(s store.CronSchedule) bool {
	return s.Where == store.CronWhereController || PushedRepo(s.RepoPath)
}

// Declared is one entry of a repository's on.schedule.
type Declared struct {
	RepoPath string
	Pipeline string
	// Name is the entry's name, [store.CronScheduleDefaultName] when the
	// entry declares none.
	Name    string
	Trigger pipelines.ScheduleTrigger
}

// ID is the schedule id this entry arms under.
func (d Declared) ID() string { return ScheduleID(d.RepoPath, d.Pipeline, d.Name) }

// DisplayName is what an operator types and reads for this entry.
func (d Declared) DisplayName() string { return displayName(d.RepoPath, d.Pipeline, d.Name) }

// Local reports whether the entry fires from a host that arms it, rather than
// from a controller.
func (d Declared) Local() bool { return d.Trigger.Where == pipelines.ScheduleWhereLocal }

// Selector is the "pipeline/name" form an operator restricts an arm with.
func (d Declared) Selector() string { return d.Pipeline + "/" + d.Name }

// DeclaredSchedules reads <repoRoot>/.sparkwing/sparkwing.yaml and returns one
// entry per declared cadence, in declaration order. A readable config that
// declares no cadence yields no schedules and no error; the config's own
// validation rejects a malformed cron before it gets here.
//
// A checkout that is gone, or a config that cannot be read, is an error rather
// than an empty answer. The two are indistinguishable to a caller that only
// counts schedules, and reading the second as the first silently disarms
// everything the host had armed.
func DeclaredSchedules(repoRoot string) ([]Declared, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", repoRoot, err)
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("read the checkout at %s: %w", root, err)
	}
	path := filepath.Join(root, ".sparkwing", projectconfig.Filename)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	cfg, err := projectconfig.Load(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("read %s: it went away while it was being read", path)
	}
	var out []Declared
	for _, p := range cfg.Pipelines {
		for i := range p.On.Schedule {
			entry := p.On.Schedule[i]
			out = append(out, Declared{
				RepoPath: root,
				Pipeline: p.Name,
				Name:     entry.EffectiveName(),
				Trigger:  entry,
			})
		}
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
	// a schedule forever. A run that has sat unclaimed for longer than
	// staleAfter is not active either: nothing is going to pick it up, and an
	// overlap policy of skip would otherwise wedge the schedule for good.
	Active(ctx context.Context, runID string, staleAfter time.Duration) (bool, error)
}

// Service evaluates schedules against one home's runs store.
//
// Set LockPath before [Service.Tick] on a host whose timer calls it: the tick
// takes an exclusive lock on that file so two timers, or a timer and a hand-run
// tick, cannot resolve the same due instant twice. Leave it empty in a caller
// that already holds exclusion of its own, such as a controller ticking under
// [store.Store.RunCronTickLeased].
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
	// Empty means the caller leases exclusion elsewhere.
	LockPath string

	// PinRoot holds the pipeline binary each locked schedule runs,
	// conventionally <home>/crons. Arming with a pin needs it.
	PinRoot string

	// ArmedBy is recorded when a schedule is first armed, such as user@host.
	ArmedBy string

	// Side names which schedules [Service.Tick] evaluates:
	// [store.CronWhereLocal], the default, or
	// [store.CronWhereController]. A row declared for the other side is
	// left alone, so two evaluators sharing one store never fire each
	// other's schedules.
	Side string
}

// safety: a row for the other side has no business firing here -- a controller
// row has no checkout to compile, a local row a clone URL the controller cannot
// resolve -- and the two can share a store.
func (s *Service) evaluates(sched store.CronSchedule) bool {
	return sideOrLocal(sched.Where) == sideOrLocal(s.Side)
}

func sideOrLocal(where string) string {
	if where == "" {
		return store.CronWhereLocal
	}
	return where
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

// LockState names what a schedule runs at its next fire.
const (
	// LockFollows compiles the checkout on every fire.
	LockFollows = "follows"
	// LockPinned runs the pinned binary, and the checkout has not moved.
	LockPinned = "pinned"
	// LockAhead runs the pinned binary while the checkout has newer commits.
	LockAhead = "ahead"
	// LockDirty runs the pinned binary while the checkout has uncommitted edits.
	LockDirty = "dirty"
	// LockMissing names a pin whose binary is gone, which fires nothing.
	LockMissing = "missing"
)

// safety: seven characters is what git itself abbreviates to, so the ref reads
// the same here as in `git log --oneline`.
const shortRefLen = 7

// Lock is what a row runs and whether the checkout has moved past it.
type Lock struct {
	Ref    string `json:"ref,omitempty"`
	Binary string `json:"binary,omitempty"`
	Digest string `json:"digest,omitempty"`
	State  string `json:"state"`
}

// ShortRef abbreviates the locked commit the way git does.
func (l Lock) ShortRef() string {
	if len(l.Ref) <= shortRefLen {
		return l.Ref
	}
	return l.Ref[:shortRefLen]
}

// Locked reports whether the schedule runs a pinned binary.
func (l Lock) Locked() bool { return l.State != LockFollows }

// Row is a stored schedule plus the fields a renderer needs and the store does
// not keep: the display name, the state, the effective cadence, the lock and
// the resolved zone.
type Row struct {
	store.CronSchedule
	// Display is the name an operator types and reads. It carries the
	// wire's "name", and the entry's own name rides on ScheduleName.
	Display      string `json:"name"`
	ScheduleName string `json:"schedule_name"`
	State        string `json:"state"`
	// StateDetail says what a locked schedule's pin is doing, empty for a
	// schedule that follows the checkout.
	StateDetail string `json:"state_detail,omitempty"`
	Lock        Lock   `json:"lock"`
	// Effective is the cadence this row actually runs: the declaration with
	// the host's override laid over it.
	Effective store.CronDeclaration `json:"effective"`
	// OverrideFields names the declared fields this host has overridden.
	OverrideFields []string `json:"override_fields,omitempty"`
	// OverrideStale reports an override whose declaration has moved under
	// it since it was set.
	OverrideStale bool           `json:"override_stale,omitempty"`
	Location      *time.Location `json:"-"`
}

func newRow(ctx context.Context, s store.CronSchedule, ck checkouts) Row {
	loc, err := trigger(s).Location()
	if err != nil {
		loc = time.UTC
	}
	lock := lockOf(ctx, s, ck)
	return Row{
		CronSchedule:   s,
		Display:        DisplayName(s),
		ScheduleName:   scheduleEntryName(s),
		State:          stateOf(s),
		StateDetail:    stateDetail(s, lock),
		Lock:           lock,
		Effective:      s.Effective(),
		OverrideFields: overrideFields(s),
		OverrideStale:  overrideStale(s),
		Location:       loc,
	}
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

func scheduleEntryName(s store.CronSchedule) string {
	if s.Name == "" {
		return store.CronScheduleDefaultName
	}
	return s.Name
}

func lockOf(ctx context.Context, s store.CronSchedule, ck checkouts) Lock {
	lock := Lock{Ref: s.LockedRef, Binary: s.LockedBinary, Digest: s.LockedDigest, State: LockFollows}
	if Pushed(s) {
		// safety: the controller clones what it was pushed, so the pin is the
		// ref alone and there is no working tree to compare it against.
		if s.LockedRef != "" {
			lock.State = LockPinned
		}
		return lock
	}
	if s.LockedBinary == "" {
		return lock
	}
	lock.State = LockPinned
	if _, err := os.Stat(s.LockedBinary); err != nil {
		lock.State = LockMissing
		return lock
	}
	if ck == nil || s.LockedRef == "" {
		return lock
	}
	state := ck.at(ctx, s.RepoPath)
	switch {
	case state.head != "" && state.head != s.LockedRef:
		lock.State = LockAhead
	case state.dirty:
		lock.State = LockDirty
	}
	return lock
}

// safety: a pushed schedule has no checkout to be ahead of, so its detail says
// which commit the controller clones rather than how a working tree compares.
func stateDetail(s store.CronSchedule, l Lock) string {
	if !Pushed(s) {
		return lockDetail(l)
	}
	if l.Ref == "" {
		return "branch tip"
	}
	return "pinned " + l.ShortRef()
}

func lockDetail(l Lock) string {
	switch l.State {
	case LockPinned:
		return "locked " + l.ShortRef()
	case LockAhead:
		return "locked, checkout ahead"
	case LockDirty:
		return "locked, checkout dirty"
	case LockMissing:
		return "locked, binary missing"
	}
	return ""
}

// safety: rebuilt so the zone, overlap and catch-up defaults resolve through
// the config validator's own code, and from the effective cadence so a host's
// override is what gets read.
func trigger(s store.CronSchedule) *pipelines.ScheduleTrigger {
	eff := s.Effective()
	catchUp := eff.CatchUp
	if catchUp <= 0 {
		catchUp = pipelines.DefaultScheduleCatchUp
	}
	where := eff.Where
	if where == "" {
		where = pipelines.ScheduleWhereLocal
	}
	return &pipelines.ScheduleTrigger{
		Name:    scheduleEntryName(s),
		Cron:    eff.Cron,
		Where:   where,
		TZ:      eff.TZ,
		Overlap: eff.Overlap,
		CatchUp: catchUp.String(),
		Args:    eff.Args,
	}
}

type evaluable struct {
	schedule *cronspec.Schedule
	loc      *time.Location
	catchUp  time.Duration
	args     map[string]string
}

func prepare(s store.CronSchedule) (evaluable, error) {
	t := trigger(s)
	// safety: an override is typed by hand after the config was validated, so
	// the effective cadence goes through the config's own validator here.
	if err := t.Validate(s.Pipeline); err != nil {
		return evaluable{}, err
	}
	parsed, err := cronspec.Parse(t.Cron)
	if err != nil {
		return evaluable{}, err
	}
	loc, err := t.Location()
	if err != nil {
		return evaluable{}, err
	}
	catchUp, err := t.CatchUpDuration()
	if err != nil {
		return evaluable{}, err
	}
	return evaluable{schedule: parsed, loc: loc, catchUp: catchUp, args: t.Args}, nil
}

func (e evaluable) nextAfter(at time.Time) *time.Time {
	next := e.schedule.Next(at, e.loc)
	if next.IsZero() {
		return nil
	}
	return &next
}

// safety: two git reads answer the whole repository, so a listing of many
// schedules in one checkout shells out once rather than once a row.
type checkouts map[string]checkoutState

type checkoutState struct {
	head  string
	dirty bool
}

func newCheckouts() checkouts { return checkouts{} }

func (c checkouts) at(ctx context.Context, root string) checkoutState {
	if state, seen := c[root]; seen {
		return state
	}
	state := readCheckout(ctx, root)
	c[root] = state
	return state
}

// safety: drift is derived on every read, so a checkout git cannot be asked
// about reports no drift rather than failing the listing.
func readCheckout(ctx context.Context, root string) checkoutState {
	var state checkoutState
	ctx, cancel := context.WithTimeout(ctx, checkoutReadTimeout)
	defer cancel()
	head, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return state
	}
	state.head = strings.TrimSpace(string(head))
	status, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain").Output()
	if err != nil {
		return state
	}
	state.dirty = strings.TrimSpace(string(status)) != ""
	return state
}

// safety: two git reads sit in front of an interactive listing, so a wedged
// repository cannot hold it open.
const checkoutReadTimeout = 10 * time.Second

// List returns every schedule this home knows, ordered by repository path,
// pipeline then schedule name, including the ones the repository no longer
// declares.
func (s *Service) List(ctx context.Context) ([]Row, error) {
	stored, err := s.Store.ListCronSchedules(ctx)
	if err != nil {
		return nil, err
	}
	ck := newCheckouts()
	out := make([]Row, 0, len(stored))
	for _, sched := range stored {
		out = append(out, newRow(ctx, sched, ck))
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
	return newRow(ctx, sched, newCheckouts()), history, nil
}

// Pause stops a schedule firing. Its cursor still advances on each tick, so
// resuming does not replay the instants that passed while it was paused.
func (s *Service) Pause(ctx context.Context, id string) error {
	return s.Store.SetCronSchedulePaused(ctx, id, true, s.now())
}

// Resume lets a paused schedule fire again from its next due instant. The
// cursor moves to now before the pause clears, so a host whose timer was off
// for the whole pause still does not replay it.
func (s *Service) Resume(ctx context.Context, id string) error {
	sched, err := s.Store.GetCronSchedule(ctx, id)
	if err != nil {
		return err
	}
	now := s.now()
	next := sched.NextDueAt
	if eval, perr := prepare(sched); perr == nil {
		next = eval.nextAfter(now)
	}
	if err := s.Store.ResolveCronDue(ctx, id, now, next, nil, now); err != nil {
		return err
	}
	return s.Store.SetCronSchedulePaused(ctx, id, false, now)
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
	if err := s.pinnedBinaryReady(sched); err != nil {
		return "", fmt.Errorf("%s: %w", DisplayName(sched), err)
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
		Args:      sched.Effective().Args,
	}
	if err := s.Store.ResolveCronDue(ctx, id, sched.CursorAt, next, fire, now); err != nil {
		return runID, fmt.Errorf("run %s launched but its fire could not be recorded: %w", runID, err)
	}
	return runID, nil
}

// ErrAmbiguousName is returned, wrapped, when a name matches more than one
// armed schedule.
var ErrAmbiguousName = errors.New("more than one schedule carries that name")

// Resolve finds one schedule from what an operator typed: a schedule id, a
// repo/pipeline[/name] display name, a pipeline/name pair, or a bare pipeline
// name that is unique across this home's schedules. An ambiguous name is an
// error naming every candidate by its display name.
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
	var byDisplay, bySelector, byPipeline []store.CronSchedule
	for _, sched := range stored {
		switch {
		case DisplayName(sched) == trimmed:
			byDisplay = append(byDisplay, sched)
		case sched.Pipeline+"/"+scheduleEntryName(sched) == trimmed:
			bySelector = append(bySelector, sched)
		case sched.Pipeline == trimmed:
			byPipeline = append(byPipeline, sched)
		}
	}
	// safety: the tiers are tried in order so a display name never loses to a
	// pipeline/name pair that happens to spell the same thing.
	for _, candidates := range [][]store.CronSchedule{byDisplay, bySelector, byPipeline} {
		switch len(candidates) {
		case 0:
			continue
		case 1:
			return candidates[0], nil
		}
		names := make([]string, 0, len(candidates))
		for _, c := range candidates {
			names = append(names, DisplayName(c))
		}
		sort.Strings(names)
		return store.CronSchedule{}, fmt.Errorf("crons: %q: %w: %s",
			trimmed, ErrAmbiguousName, strings.Join(names, ", "))
	}
	return store.CronSchedule{}, fmt.Errorf(
		"crons: no schedule named %q is armed here; `sparkwing crons list` names them", trimmed)
}

// safety: the order matches the CLI's own flags, so a reader sees the fields
// named the way they were set.
func overrideFields(s store.CronSchedule) []string {
	if s.Override == nil {
		return nil
	}
	var out []string
	if s.Override.Cron != "" {
		out = append(out, "cron")
	}
	if s.Override.TZ != "" {
		out = append(out, "tz")
	}
	if s.Override.Overlap != "" {
		out = append(out, "overlap")
	}
	if s.Override.CatchUp != nil {
		out = append(out, "catch_up")
	}
	if s.Override.Args != nil {
		out = append(out, "args")
	}
	return out
}

// safety: only the cadence is compared. The lock moves at an explicit install,
// lock or unlock, and none of those is the repository changing its mind about
// what the schedule should do.
func overrideStale(s store.CronSchedule) bool {
	if s.Override == nil {
		return false
	}
	base, now := s.Override.Base, s.Declaration()
	return base.Cron != now.Cron ||
		base.TZ != now.TZ ||
		base.Overlap != now.Overlap ||
		base.CatchUp != now.CatchUp ||
		base.Where != now.Where ||
		!maps.Equal(base.Args, now.Args)
}

// safety: a pinned binary that is gone cannot be recompiled from the checkout,
// because the checkout is exactly what the pin exists to ignore.
func (s *Service) pinnedBinaryReady(sched store.CronSchedule) error {
	if sched.LockedBinary == "" {
		return nil
	}
	if _, err := os.Stat(sched.LockedBinary); err != nil {
		return fmt.Errorf(
			"the pinned pipeline binary at %s is gone; re-run `sparkwing crons install` to pin the checkout again, "+
				"or `sparkwing crons unlock` to follow it", sched.LockedBinary)
	}
	return nil
}

// ScheduleEnvKey carries the id of the cron schedule that launched a run, so
// one run traces back to the cadence that asked for it. The cron_fires table is
// the authoritative join; this key answers the question from the run's own row,
// which is where an operator reading `runs get` starts.
const ScheduleEnvKey = "_SPARKWING_CRON_SCHEDULE"

// PinnedBinaryEnvKey carries the pipeline binary a locked cron schedule pinned
// at arming time. The consumer execs that file instead of compiling the
// checkout, so a checkout updated between arming and three in the morning
// cannot change what the unattended run executes.
const PinnedBinaryEnvKey = "_SPARKWING_CRON_BINARY"

// PinnedDigestEnvKey carries the sha256 of that file as it stood when the
// schedule was armed. The consumer recomputes it before executing, so a pinned
// binary replaced in place fails the run instead of running as the pin.
const PinnedDigestEnvKey = "_SPARKWING_CRON_BINARY_SHA256"
