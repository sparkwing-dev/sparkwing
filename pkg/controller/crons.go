package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// CronTickInterval is how often a controller offers to evaluate the schedules
// pushed to it. It is shorter than a minute so a wake that the store's lease
// turns away is followed by another inside the same minute; the lease, not this
// interval, is what keeps one evaluator per store per minute.
const CronTickInterval = 20 * time.Second

// safety: long enough that a tick which dies mid-flight does not hand the next
// minute a claim it will never release, short enough that the minute after that
// still runs.
const cronTickLeaseTTL = 90 * time.Second

// safety: the wire word a scheduled run carries, bare and without a host
// suffix, because pipelines branch on it.
const cronTriggerSource = "schedule"

const cronDetailFires = 20

const cronDetailUpcoming = 5

// safety: a scheduled launch writes what an HTTP submission writes, so the
// cluster claims, clones and executes it by exactly the same machinery.
type cronLauncher struct {
	server *Server
}

// Launch records the trigger, the pending run and the dispatch for one due
// instant. The idempotency key is the schedule and the instant, so a second
// tick that resolves the same minute reaches the first run instead of starting
// a second.
func (l cronLauncher) Launch(ctx context.Context, s store.CronSchedule, due time.Time) (string, error) {
	repoURL, err := sourceurl.ValidateCloneURL(s.RepoPath)
	if err != nil {
		return "", fmt.Errorf("the schedule's repository %q is not a clone URL: %w", s.RepoPath, err)
	}
	key := CronIdempotencyKey(s.ID, due)
	runID := newRunID()
	err = l.server.admitTrigger(ctx, triggerIntake{
		RunID:    runID,
		Pipeline: s.Pipeline,
		Args:     s.Effective().Args,
		Source:   cronTriggerSource,
		Env:      map[string]string{crons.ScheduleEnvKey: s.ID},
		Git: triggerReqGit{
			Branch:  s.GitBranch,
			SHA:     s.LockedRef,
			RepoURL: repoURL,
		},
		IdempotencyKey: key,
		At:             time.Now(),
	})
	if errors.Is(err, store.ErrDuplicateIdempotencyKey) {
		existing, ferr := l.server.store.FindTriggerByIdempotencyKey(ctx, s.Pipeline, key)
		if ferr != nil {
			return "", fmt.Errorf("another tick already resolved %s, and its run could not be read: %w", key, ferr)
		}
		return existing.ID, nil
	}
	if err != nil {
		return "", err
	}
	return runID, nil
}

// Active reports whether a scheduled run is still pending or running. A run
// nothing has claimed for longer than staleAfter is not active: nothing is
// going to pick it up, and a schedule whose policy is skip would otherwise stop
// firing for good.
func (l cronLauncher) Active(ctx context.Context, runID string, staleAfter time.Duration) (bool, error) {
	run, err := l.server.store.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if run == nil {
		return false, nil
	}
	switch run.Status {
	case "running":
		return true, nil
	case "pending":
		return time.Since(cronRunQueuedAt(run)) <= staleAfter, nil
	}
	return false, nil
}

// safety: CreatedAt is the intake stamp; StartedAt is all a row written before
// that column has.
func cronRunQueuedAt(run *store.Run) time.Time {
	if !run.CreatedAt.IsZero() {
		return run.CreatedAt
	}
	return run.StartedAt
}

// CronIdempotencyKey is the deduplication token a scheduled launch carries: the
// schedule and the instant it resolved, so two evaluators of one minute reach
// one run.
func CronIdempotencyKey(scheduleID string, due time.Time) string {
	return scheduleID + "@" + due.UTC().Format(time.RFC3339)
}

func (s *Server) cronService() *crons.Service {
	return &crons.Service{
		Store:    s.store,
		Launcher: cronLauncher{server: s},
		Now:      s.cronNow,
		Host:     s.cronHolder,
		Version:  buildinfo.Read("sparkwing-controller", "").Version,
	}
}

func defaultCronHolder() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "controller"
	}
	return host
}

// safety: the lease the store hands one holder at a time is what keeps several
// controllers sharing a store from firing one due instant twice; a tick that
// fails is logged so the next interval still runs.
func (s *Server) runCronTick(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cronTickOnce(ctx)
		}
	}
}

func (s *Server) cronTickOnce(ctx context.Context) {
	var report crons.TickReport
	ran, err := s.store.RunCronTickLeased(ctx, s.cronHolder, cronTickLeaseTTL, func(tickCtx context.Context) error {
		var terr error
		report, terr = s.cronService().Tick(tickCtx, false)
		return terr
	})
	switch {
	case err != nil:
		s.logger.Error("cron tick failed", "holder", s.cronHolder, "err", err)
	case !ran:
		return
	default:
		s.logger.Info("cron tick", "holder", s.cronHolder, "summary", report.Summary())
	}
	for _, e := range report.Errors {
		s.logger.Warn("cron schedule could not be evaluated", "detail", e)
	}
	for _, d := range report.Decisions {
		if d.RunID != "" {
			s.logger.Info("cron schedule fired",
				"schedule", crons.DisplayName(d.Schedule), "run_id", d.RunID,
				"due", d.Due.UTC().Format(time.RFC3339))
		}
	}
}

// safety: the body is the whole set, so a schedule it does not carry is
// withdrawn; Follow ignores SHA and clones the tip of Branch at each fire.
type cronRepoRequest struct {
	RepoURL   string             `json:"repo_url"`
	Branch    string             `json:"branch,omitempty"`
	SHA       string             `json:"sha,omitempty"`
	Follow    bool               `json:"follow,omitempty"`
	Schedules []cronRepoSchedule `json:"schedules"`
}

// safety: spelled the way the repository declares an entry, so CatchUp is a Go
// duration such as "6h" rather than a nanosecond count.
type cronRepoSchedule struct {
	Pipeline string            `json:"pipeline"`
	Name     string            `json:"name,omitempty"`
	Cron     string            `json:"cron"`
	TZ       string            `json:"tz,omitempty"`
	Overlap  string            `json:"overlap,omitempty"`
	CatchUp  string            `json:"catch_up,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
}

type cronReposResponse struct {
	Schedules []crons.ScheduleView `json:"schedules"`
	Withdrawn []string             `json:"withdrawn,omitempty"`
}

type cronRepoDeleteResponse struct {
	RepoURL string `json:"repo_url"`
	Removed int    `json:"removed"`
}

// safety: a member left empty keeps the declared value; Args replaces the
// declared argument set whole, which is what an empty non-nil map means.
type cronOverrideRequest struct {
	Cron    string            `json:"cron,omitempty"`
	TZ      string            `json:"tz,omitempty"`
	Overlap string            `json:"overlap,omitempty"`
	CatchUp string            `json:"catch_up,omitempty"`
	Args    map[string]string `json:"args,omitempty"`
}

func (s *Server) handleListCrons(w http.ResponseWriter, r *http.Request) {
	svc := s.cronService()
	health, err := svc.ControllerHealth(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read cron health: %w", err))
		return
	}
	rows, err := svc.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("list cron schedules: %w", err))
		return
	}
	out := crons.OverviewView{
		Health:    crons.NewHealthView(health),
		Schedules: make([]crons.ScheduleView, 0, len(rows)),
	}
	for _, row := range rows {
		out.Schedules = append(out.Schedules, crons.NewScheduleView(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCron(w http.ResponseWriter, r *http.Request) {
	svc := s.cronService()
	sched, ok := s.resolveCron(w, r, svc)
	if !ok {
		return
	}
	row, fires, err := svc.Show(r.Context(), sched.ID, cronDetailFires)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read cron schedule: %w", err))
		return
	}
	upcoming, err := svc.Upcoming(r.Context(), sched.ID, cronDetailUpcoming)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read upcoming instants: %w", err))
		return
	}
	out := crons.DetailView{
		Schedule: crons.NewScheduleView(row),
		Fires:    make([]crons.FireView, 0, len(fires)),
		Upcoming: make([]string, 0, len(upcoming)),
	}
	for _, fire := range fires {
		out.Fires = append(out.Fires, crons.NewFireView(fire, s.cronRunStatus(r.Context(), fire.RunID)))
	}
	for _, at := range upcoming {
		out.Upcoming = append(out.Upcoming, crons.RFC3339(at))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutCronRepo(w http.ResponseWriter, r *http.Request) {
	var body cronRepoRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	repoURL, err := sourceurl.ValidateCloneURL(body.RepoURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("repo_url: %w", err))
		return
	}
	sha := body.SHA
	if body.Follow {
		sha = ""
	}
	if sha != "" {
		if err := validateGitSHA(sha); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	entries, err := declaredFromRequest(body.Schedules)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	report, err := s.cronService().ArmPushed(r.Context(), crons.ArmPush{
		RepoURL: repoURL,
		Branch:  body.Branch,
		SHA:     sha,
		Entries: entries,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	out := cronReposResponse{
		Schedules: make([]crons.ScheduleView, 0, len(report.Schedules)),
		Withdrawn: report.Withdrawals,
	}
	svc := s.cronService()
	for _, sched := range report.Schedules {
		row, _, rerr := svc.Show(r.Context(), sched.ID, 1)
		if rerr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("read cron schedule: %w", rerr))
			return
		}
		out.Schedules = append(out.Schedules, crons.NewScheduleView(row))
	}
	writeJSON(w, http.StatusOK, out)
}

// safety: the entries are validated as a set before anything is written, so a
// push carrying one bad cadence never half-arms the repository.
func declaredFromRequest(schedules []cronRepoSchedule) ([]crons.Declared, error) {
	entries := make([]crons.Declared, 0, len(schedules))
	for _, entry := range schedules {
		if entry.Pipeline == "" {
			return nil, errors.New("every schedule needs a pipeline")
		}
		name := entry.Name
		if name == "" {
			name = store.CronScheduleDefaultName
		}
		trigger := pipelines.ScheduleTrigger{
			Name:    name,
			Cron:    entry.Cron,
			TZ:      entry.TZ,
			Overlap: entry.Overlap,
			CatchUp: entry.CatchUp,
			Where:   pipelines.ScheduleWhereController,
			Args:    entry.Args,
		}
		if err := trigger.Validate(entry.Pipeline); err != nil {
			return nil, err
		}
		entries = append(entries, crons.Declared{Pipeline: entry.Pipeline, Name: name, Trigger: trigger})
	}
	return entries, nil
}

func (s *Server) handleDeleteCronRepo(w http.ResponseWriter, r *http.Request) {
	repoURL, err := sourceurl.ValidateCloneURL(r.URL.Query().Get("repo_url"))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("repo_url: %w", err))
		return
	}
	removed, err := s.cronService().DisarmRepoURL(r.Context(), repoURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cronRepoDeleteResponse{RepoURL: repoURL, Removed: removed})
}

func (s *Server) handlePauseCron(w http.ResponseWriter, r *http.Request) {
	s.setCronPaused(w, r, true)
}

func (s *Server) handleResumeCron(w http.ResponseWriter, r *http.Request) {
	s.setCronPaused(w, r, false)
}

func (s *Server) setCronPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	svc := s.cronService()
	sched, ok := s.resolveCron(w, r, svc)
	if !ok {
		return
	}
	var err error
	if paused {
		err = svc.Pause(r.Context(), sched.ID)
	} else {
		err = svc.Resume(r.Context(), sched.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("update cron schedule: %w", err))
		return
	}
	s.writeCronSchedule(w, r, svc, sched.ID)
}

func (s *Server) handleRunCronNow(w http.ResponseWriter, r *http.Request) {
	svc := s.cronService()
	sched, ok := s.resolveCron(w, r, svc)
	if !ok {
		return
	}
	runID, err := svc.RunNow(r.Context(), sched.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("launch cron schedule: %w", err))
		return
	}
	row, _, err := svc.Show(r.Context(), sched.ID, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read cron schedule: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, crons.RunEnvelope{RunID: runID, Schedule: crons.NewScheduleView(row)})
}

func (s *Server) handleDisarmCron(w http.ResponseWriter, r *http.Request) {
	svc := s.cronService()
	sched, ok := s.resolveCron(w, r, svc)
	if !ok {
		return
	}
	row, _, err := svc.Show(r.Context(), sched.ID, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read cron schedule: %w", err))
		return
	}
	if err := svc.Disarm(r.Context(), sched.ID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("disarm cron schedule: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, crons.ScheduleEnvelope{Schedule: crons.NewScheduleView(row)})
}

func (s *Server) handleSetCronOverride(w http.ResponseWriter, r *http.Request) {
	var body cronOverrideRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	fields := crons.Override{}
	if body.Cron != "" {
		fields.Cron = &body.Cron
	}
	if body.TZ != "" {
		fields.TZ = &body.TZ
	}
	if body.Overlap != "" {
		fields.Overlap = &body.Overlap
	}
	if body.CatchUp != "" {
		d, perr := time.ParseDuration(body.CatchUp)
		if perr != nil {
			writeError(w, http.StatusBadRequest,
				fmt.Errorf("catch_up %q: expected a duration such as 6h", body.CatchUp))
			return
		}
		fields.CatchUp = &d
	}
	if body.Args != nil {
		fields.Args = body.Args
	}
	if fields.Empty() {
		writeError(w, http.StatusBadRequest,
			errors.New("name at least one of cron, tz, overlap, catch_up or args"))
		return
	}
	svc := s.cronService()
	sched, ok := s.resolveCron(w, r, svc)
	if !ok {
		return
	}
	row, err := svc.SetOverride(r.Context(), sched.ID, fields)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, crons.ScheduleEnvelope{Schedule: crons.NewScheduleView(row)})
}

func (s *Server) handleClearCronOverride(w http.ResponseWriter, r *http.Request) {
	svc := s.cronService()
	sched, ok := s.resolveCron(w, r, svc)
	if !ok {
		return
	}
	row, err := svc.ClearOverride(r.Context(), sched.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, crons.ScheduleEnvelope{Schedule: crons.NewScheduleView(row)})
}

func (s *Server) resolveCron(w http.ResponseWriter, r *http.Request, svc *crons.Service) (store.CronSchedule, bool) {
	sched, err := svc.Resolve(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return store.CronSchedule{}, false
	}
	return sched, true
}

func (s *Server) writeCronSchedule(w http.ResponseWriter, r *http.Request, svc *crons.Service, id string) {
	row, _, err := svc.Show(r.Context(), id, 1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read cron schedule: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, crons.ScheduleEnvelope{Schedule: crons.NewScheduleView(row)})
}

// safety: a run the store no longer holds reads as no status, not an error; the
// fire is still history.
func (s *Server) cronRunStatus(ctx context.Context, runID string) string {
	if runID == "" {
		return ""
	}
	run, err := s.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return ""
	}
	return run.Status
}
