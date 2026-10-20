package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// safety: shorter than a minute, so a wake the store's lease turns away is
// followed by another inside the same minute. store.CronTickWindow, not this
// interval, is what spaces the ticks that actually run.
const cronTickOffer = 20 * time.Second

// safety: long enough that a tick which dies mid-flight does not hand the next
// minute a claim it will never release, short enough that the minute after that
// still runs.
const cronTickLeaseTTL = 90 * time.Second

const cronDetailFires = 200

const cronDetailUpcoming = 5

type cronLauncher struct {
	server *Server
}

// ScheduledRunWriter admits a stored schedule through the sealed intake transaction.
type ScheduledRunWriter interface {
	Team() store.Team
	AdmitScheduledRun(context.Context, store.CronSchedule, time.Time) (store.AdmissionResult, error)
}

// Launch records one run for a due schedule instant.
func (l cronLauncher) Launch(ctx context.Context, s store.CronSchedule, due time.Time) (string, error) {
	if s.Team == "" || s.ID == "" || due.IsZero() {
		return "", invalidEngineBody("schedule team, id and due instant are required")
	}
	writer, err := tenantPort[ScheduledRunWriter](ctx, l.server, s.Team)
	if err != nil {
		return "", err
	}
	admitted, err := writer.AdmitScheduledRun(ctx, s, due)
	if err != nil {
		return "", err
	}
	if admitted.RunID == "" {
		return "", missingEndpointPolicy()
	}
	if !admitted.Replayed {
		l.server.recordQueueActivity(admitted.Request.AdmittedAt)
	}
	return admitted.RunID, nil
}

// Active reports whether a scheduled run is still pending or running. A run
// nothing has claimed for longer than staleAfter is not active: nothing is
// going to pick it up, and a schedule whose policy is skip would otherwise stop
// firing for good.
func (l cronLauncher) Active(ctx context.Context, sched store.CronSchedule, runID string, staleAfter time.Duration) (bool, error) {
	tenant, err := l.server.tenantForTeam(ctx, sched.Team)
	if err != nil {
		return false, err
	}
	run, err := tenant.GetRun(ctx, runID)
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
		return time.Since(run.CreatedAt) <= staleAfter, nil
	}
	return false, nil
}

// safety: Each tick launches a schedule in its own team, never the service's team.
func (s *Server) cronService() *crons.Service {
	return s.cronServiceFor(nil, "")
}

// safety: a route acts on its caller's team alone, so its service reads and
// writes schedules only through that team's handle, and the principal that
// armed a schedule owns the runs it fires, so the per-principal guards measure
// it rather than leaving cron launches unowned.
func (s *Server) cronServiceFor(tenant *store.Tenant, principal string) *crons.Service {
	if s.localSchedules != nil && tenant != nil && tenant.Team() == store.DefaultTeam {
		return s.localSchedules
	}

	svc := &crons.Service{
		Store:    s.store,
		Launcher: cronLauncher{server: s},
		Now:      s.cronNow,
		Host:     s.cronHolder,
		Version:  buildinfo.Read("sparkwing-controller", "").Version,
		Side:     store.CronWhereController,
		ArmedBy:  principal,
	}
	if tenant != nil {
		svc.Schedules = tenant
	}
	return svc
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
	ran, err := s.operator.RunCronTickLeased(ctx, s.cronHolder, cronTickLeaseTTL, func(tickCtx context.Context) error {
		// safety: a tick that outlives the lease is a tick running beside
		// whoever claimed the next minute, so it is cut at the lease instead.
		deadline, cancel := context.WithTimeout(tickCtx, cronTickLeaseTTL)
		defer cancel()
		var terr error
		report, terr = s.cronService().Tick(deadline, false)
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

func (s *Server) handleListCrons(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	next, err := scheduleCount(r, "next", 0)
	if err != nil {
		return err
	}
	all := false
	if values, exists := r.URL.Query()["all"]; exists {
		if len(values) != 1 || values[0] != "true" && values[0] != "false" {
			return invalidEngineBody("all must be true or false")
		}
		all = values[0] == "true"
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	svc := s.cronServiceFor(tenant, "")
	health, err := svc.ControllerHealth(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	rows, err := svc.List(r.Context())
	if err != nil {
		return engineLookupError(err)
	}
	out := controllerwire.ScheduleOverview{
		Health:    crons.NewHealthView(health),
		Schedules: make([]controllerwire.ScheduleView, 0, len(rows)),
		Upcoming:  []controllerwire.ScheduleUpcoming{},
	}
	for _, row := range rows {
		if !all && !row.Declared {
			continue
		}
		out.Schedules = append(out.Schedules, crons.NewScheduleView(row))
		if next > 0 {
			instants, err := svc.Upcoming(r.Context(), row.ID, next)
			if err != nil {
				return engineLookupError(err)
			}
			for _, at := range instants {
				out.Upcoming = append(out.Upcoming, controllerwire.ScheduleUpcoming{Schedule: row.ID, Name: row.Display, At: crons.RFC3339(at)})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleGetCron(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	firesCount, err := scheduleCount(r, "fires", cronDetailFires)
	if err != nil {
		return err
	}
	next, err := scheduleCount(r, "next", cronDetailUpcoming)
	if err != nil {
		return err
	}
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return engineLookupError(err)
	}
	svc := s.cronServiceFor(tenant, "")
	sched, err := svc.Resolve(r.Context(), r.PathValue("schedule"))
	if err != nil {
		return engineLookupError(err)
	}
	row, fires, err := svc.Show(r.Context(), sched.ID, firesCount)
	if err != nil {
		return engineLookupError(err)
	}
	upcoming, err := svc.Upcoming(r.Context(), sched.ID, next)
	if err != nil {
		return engineLookupError(err)
	}
	out := controllerwire.DetailView{
		Schedule: crons.NewScheduleView(row),
		Fires:    make([]controllerwire.FireView, 0, len(fires)),
		Upcoming: make([]string, 0, len(upcoming)),
	}
	if firesCount == 0 {
		fires = nil
	}
	for _, fire := range fires {
		out.Fires = append(out.Fires, crons.NewFireView(fire, cronRunStatus(r.Context(), tenant, fire.RunID)))
	}
	for _, at := range upcoming {
		out.Upcoming = append(out.Upcoming, crons.RFC3339(at))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// safety: the branch reaches git as the ref a fire clones, so it is held to
// git's own check-ref-format rules here rather than at three in the morning.
// An empty branch is allowed: a push pinned to a SHA needs none.
func validateGitBranchName(branch string) error {
	if branch == "" {
		return nil
	}
	if len(branch) > maxGitBranchLen {
		return fmt.Errorf("must be at most %d characters", maxGitBranchLen)
	}
	if strings.HasPrefix(branch, "-") {
		return errors.New("must not begin with '-', which git reads as an option")
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") ||
		strings.HasPrefix(branch, ".") || strings.HasSuffix(branch, ".") {
		return errors.New("must not begin or end with '/' or '.'")
	}
	if strings.HasSuffix(branch, ".lock") || strings.Contains(branch, "//") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") {
		return errors.New(`must not contain "..", "//" or "@{", or end in ".lock"`)
	}
	for _, r := range branch {
		if r <= ' ' || r == 0x7f {
			return errors.New("must not contain a space or a control character")
		}
		if strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("must not contain %q", r)
		}
	}
	return nil
}

// safety: git itself imposes no limit, but a ref this long is a mistake rather
// than a branch, and the value is stored and rendered in a column.
const maxGitBranchLen = 255

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

// safety: a run the store no longer holds reads as no status, not an error; the
// fire is still history.
func cronRunStatus(ctx context.Context, tenant *store.Tenant, runID string) string {
	if runID == "" {
		return ""
	}
	run, err := tenant.GetRun(ctx, runID)
	if err != nil || run == nil {
		return ""
	}
	return run.Status
}

// safety: every schedule is a stream of runs the team pays for and the
// controller evaluates each minute, so a push is bounded per repository and a
// team's repositories are bounded in count. Re-pushing a repository the team
// already arms replaces its schedules and does not count as another.
const (
	maxCronSchedulesPerRepo = limits.MaxSchedulesPerRepo
	maxCronReposPerTeam     = limits.MaxScheduledReposPerTeam
)

func (s *Server) cronRepoCapRefusal(ctx context.Context, svc *crons.Service, repoURL string, githubRepoID int64, entries int) (int, error) {
	if entries > maxCronSchedulesPerRepo {
		return http.StatusBadRequest, fmt.Errorf(
			"a repository may declare at most %d schedules; this push declares %d", maxCronSchedulesPerRepo, entries)
	}
	rows, err := svc.List(ctx)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("list cron schedules: %w", err)
	}
	repos := map[string]bool{}
	for _, row := range rows {
		if !row.Declared {
			continue
		}
		key := row.RepoPath
		if row.GitHubRepositoryID > 0 {
			key = fmt.Sprintf("github:%d", row.GitHubRepositoryID)
		}
		repos[key] = true
	}
	key := repoURL
	if githubRepoID > 0 {
		key = fmt.Sprintf("github:%d", githubRepoID)
	}
	if !repos[key] && len(repos) >= maxCronReposPerTeam {
		return http.StatusConflict, fmt.Errorf(
			"this team already schedules %d repositories, the most it may; disarm one first", len(repos))
	}
	return 0, nil
}
