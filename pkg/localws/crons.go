package localws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	cronDetailFires    = 20
	cronDetailUpcoming = 5
)

const cronRequestTimeout = 10 * time.Second

// safety: the launch persists a trigger and may start a consumer, so it needs longer than a read.
const cronRunTimeout = 60 * time.Second

// safety: mirrors [crontimer.State] so the dashboard's wire contract does not move when that struct does.
type cronTimerStateDTO struct {
	Installed bool   `json:"installed"`
	Foreign   bool   `json:"foreign"`
	Enabled   bool   `json:"enabled"`
	Stale     bool   `json:"stale"`
	Path      string `json:"path"`
	Binary    string `json:"binary"`
	Detail    string `json:"detail"`
}

type cronTickDTO struct {
	// safety: empty until this home has recorded a tick.
	At      string `json:"at"`
	Host    string `json:"host"`
	Version string `json:"version"`
	Error   string `json:"error"`
}

type cronHealthDTO struct {
	Timer      cronTimerStateDTO `json:"timer"`
	LastTick   cronTickDTO       `json:"last_tick"`
	TickStale  bool              `json:"tick_stale"`
	Schedules  int               `json:"schedules"`
	Armed      int               `json:"armed"`
	Paused     int               `json:"paused"`
	Undeclared int               `json:"undeclared"`
	Detail     string            `json:"detail"`
}

type cronScheduleDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	RepoPath    string  `json:"repo_path"`
	Pipeline    string  `json:"pipeline"`
	Cron        string  `json:"cron"`
	TZ          string  `json:"tz"`
	Overlap     string  `json:"overlap"`
	CatchUpNS   int64   `json:"catch_up_ns"`
	Paused      bool    `json:"paused"`
	Declared    bool    `json:"declared"`
	State       string  `json:"state"`
	ArmedAt     string  `json:"armed_at"`
	UpdatedAt   string  `json:"updated_at"`
	LastFiredAt *string `json:"last_fired_at"`
	LastRunID   string  `json:"last_run_id"`
	LastOutcome string  `json:"last_outcome"`
	NextDueAt   *string `json:"next_due_at"`
}

type cronFireDTO struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	DueAt      string `json:"due_at"`
	DecidedAt  string `json:"decided_at"`
	Outcome    string `json:"outcome"`
	RunID      string `json:"run_id"`
	Detail     string `json:"detail"`
	// safety: empty when the fire launched nothing or its run has since been pruned.
	RunStatus string `json:"run_status"`
}

type cronsOverviewDTO struct {
	Health    cronHealthDTO     `json:"health"`
	Schedules []cronScheduleDTO `json:"schedules"`
}

type cronDetailDTO struct {
	Schedule cronScheduleDTO `json:"schedule"`
	Fires    []cronFireDTO   `json:"fires"`
	Upcoming []string        `json:"upcoming"`
}

type cronScheduleEnvelope struct {
	Schedule cronScheduleDTO `json:"schedule"`
}

type cronRunEnvelope struct {
	RunID    string          `json:"run_id"`
	Schedule cronScheduleDTO `json:"schedule"`
}

type cronsAPI struct {
	store    *store.Store
	paths    orchestrator.Paths
	readOnly bool
}

// safety: the controller catch-all at /api/v1/ claims any route not named here, and the local mux
// does not wrap these handlers, so read-only is enforced inside them.
func registerCronRoutes(mux *http.ServeMux, api *cronsAPI) {
	mux.Handle("GET /api/v1/crons", http.HandlerFunc(api.overview))
	mux.Handle("GET /api/v1/crons/{id}", http.HandlerFunc(api.detail))
	mux.Handle("POST /api/v1/crons/{id}/pause", http.HandlerFunc(api.pause))
	mux.Handle("POST /api/v1/crons/{id}/resume", http.HandlerFunc(api.resume))
	mux.Handle("POST /api/v1/crons/{id}/run", http.HandlerFunc(api.runNow))
}

func (a *cronsAPI) overview(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.service(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cronRequestTimeout)
	defer cancel()

	health, err := svc.Health(ctx, dashboardTimerHost())
	if err != nil {
		http.Error(w, "read cron health: "+err.Error(), http.StatusBadGateway)
		return
	}
	rows, err := svc.List(ctx)
	if err != nil {
		http.Error(w, "list cron schedules: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := cronsOverviewDTO{Health: cronHealth(health), Schedules: make([]cronScheduleDTO, 0, len(rows))}
	for _, row := range rows {
		out.Schedules = append(out.Schedules, cronSchedule(row))
	}
	writeJSON(w, out)
}

func (a *cronsAPI) detail(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.service(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cronRequestTimeout)
	defer cancel()

	sched, ok := resolveCron(ctx, w, svc, r.PathValue("id"))
	if !ok {
		return
	}
	row, fires, err := svc.Show(ctx, sched.ID, cronDetailFires)
	if err != nil {
		http.Error(w, "read cron schedule: "+err.Error(), http.StatusBadGateway)
		return
	}
	upcoming, err := svc.Upcoming(ctx, sched.ID, cronDetailUpcoming)
	if err != nil {
		http.Error(w, "read upcoming instants: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := cronDetailDTO{
		Schedule: cronSchedule(row),
		Fires:    make([]cronFireDTO, 0, len(fires)),
		Upcoming: make([]string, 0, len(upcoming)),
	}
	for _, fire := range fires {
		out.Fires = append(out.Fires, cronFire(ctx, a.store, fire))
	}
	for _, at := range upcoming {
		out.Upcoming = append(out.Upcoming, rfc3339(at))
	}
	writeJSON(w, out)
}

func (a *cronsAPI) pause(w http.ResponseWriter, r *http.Request) {
	a.setPaused(w, r, true)
}

func (a *cronsAPI) resume(w http.ResponseWriter, r *http.Request) {
	a.setPaused(w, r, false)
}

func (a *cronsAPI) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	svc, ok := a.writableService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cronRequestTimeout)
	defer cancel()

	sched, ok := resolveCron(ctx, w, svc, r.PathValue("id"))
	if !ok {
		return
	}
	var err error
	if paused {
		err = svc.Pause(ctx, sched.ID)
	} else {
		err = svc.Resume(ctx, sched.ID)
	}
	if err != nil {
		http.Error(w, "update cron schedule: "+err.Error(), http.StatusBadGateway)
		return
	}
	row, ok := reloadCron(ctx, w, svc, sched.ID)
	if !ok {
		return
	}
	writeJSON(w, cronScheduleEnvelope{Schedule: cronSchedule(row)})
}

func (a *cronsAPI) runNow(w http.ResponseWriter, r *http.Request) {
	svc, ok := a.writableService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), cronRunTimeout)
	defer cancel()

	sched, ok := resolveCron(ctx, w, svc, r.PathValue("id"))
	if !ok {
		return
	}
	runID, err := cronRunNow(ctx, a.paths.Root, sched.ID)
	if err != nil {
		http.Error(w, "launch cron schedule: "+err.Error(), http.StatusBadGateway)
		return
	}
	row, ok := reloadCron(ctx, w, svc, sched.ID)
	if !ok {
		return
	}
	writeJSON(w, cronRunEnvelope{RunID: runID, Schedule: cronSchedule(row)})
}

// safety: the dashboard never ticks and never launches in process, so this service carries
// neither a lock path nor a launcher.
func (a *cronsAPI) service(w http.ResponseWriter) (*crons.Service, bool) {
	if a.store == nil {
		http.Error(w, "this dashboard has no local runs store, so it cannot read schedules",
			http.StatusBadGateway)
		return nil, false
	}
	return &crons.Service{Store: a.store}, true
}

func (a *cronsAPI) writableService(w http.ResponseWriter) (*crons.Service, bool) {
	if a.readOnly {
		http.Error(w, "dashboard is read-only", http.StatusForbidden)
		return nil, false
	}
	return a.service(w)
}

func resolveCron(ctx context.Context, w http.ResponseWriter, svc *crons.Service, id string) (store.CronSchedule, bool) {
	sched, err := svc.Resolve(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return store.CronSchedule{}, false
	}
	return sched, true
}

func reloadCron(ctx context.Context, w http.ResponseWriter, svc *crons.Service, id string) (crons.Row, bool) {
	row, _, err := svc.Show(ctx, id, 1)
	if err != nil {
		http.Error(w, "read cron schedule: "+err.Error(), http.StatusBadGateway)
		return crons.Row{}, false
	}
	return row, true
}

// safety: tests replace this instead of launching out of process.
var cronRunNow = execCronRunNow

// safety: the detached submission path lives in package main, which no library can import, so the
// same binary is re-executed rather than grown a second copy that can drift.
func execCronRunNow(ctx context.Context, home, scheduleID string) (string, error) {
	self, err := installsite.Self()
	if err != nil {
		return "", fmt.Errorf("resolve this sparkwing binary: %w", err)
	}
	cmd := exec.CommandContext(ctx, self, "crons", "run", scheduleID, "-o", "json")
	cmd.Env = append(os.Environ(), "SPARKWING_HOME="+home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("crons run %s: %w: %s", scheduleID, err, detail)
	}
	dec := json.NewDecoder(&stdout)
	for {
		var report struct {
			RunID string `json:"run_id"`
		}
		if derr := dec.Decode(&report); derr != nil {
			break
		}
		if report.RunID != "" {
			return report.RunID, nil
		}
	}
	return "", fmt.Errorf("crons run %s named no run: %s", scheduleID, strings.TrimSpace(stdout.String()))
}

// safety: a machine whose home cannot be resolved is described as no platform, which
// [crontimer.Status] reports as unsupported rather than failing the whole health read.
func dashboardTimerHost() crontimer.Host {
	home, err := os.UserHomeDir()
	if err != nil {
		return crontimer.Host{}
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	binary, err := installsite.Self()
	if err != nil {
		binary = ""
	}
	return crontimer.Host{
		GOOS:       runtime.GOOS,
		Home:       home,
		ConfigHome: configHome,
		Binary:     binary,
		UID:        os.Getuid(),
		Exec:       crontimer.DefaultExec,
	}
}

func cronHealth(h crons.Health) cronHealthDTO {
	return cronHealthDTO{
		Timer: cronTimerStateDTO{
			Installed: h.Timer.Installed,
			Foreign:   h.Timer.Foreign,
			Enabled:   h.Timer.Enabled,
			Stale:     h.Timer.Stale,
			Path:      h.Timer.Path,
			Binary:    h.Timer.Binary,
			Detail:    h.Timer.Detail,
		},
		LastTick: cronTickDTO{
			At:      rfc3339(h.LastTick.At),
			Host:    h.LastTick.Host,
			Version: h.LastTick.Version,
			Error:   h.LastTick.Error,
		},
		TickStale:  h.TickStale,
		Schedules:  h.Schedules,
		Armed:      h.Armed,
		Paused:     h.Paused,
		Undeclared: h.Undeclared,
		Detail:     h.Detail,
	}
}

func cronSchedule(row crons.Row) cronScheduleDTO {
	return cronScheduleDTO{
		ID:          row.ID,
		Name:        row.Name,
		RepoPath:    row.RepoPath,
		Pipeline:    row.Pipeline,
		Cron:        row.Cron,
		TZ:          row.TZ,
		Overlap:     row.Overlap,
		CatchUpNS:   int64(row.CatchUp),
		Paused:      row.Paused,
		Declared:    row.Declared,
		State:       row.State,
		ArmedAt:     rfc3339(row.ArmedAt),
		UpdatedAt:   rfc3339(row.UpdatedAt),
		LastFiredAt: rfc3339Ptr(row.LastFiredAt),
		LastRunID:   row.LastRunID,
		LastOutcome: row.LastOutcome,
		NextDueAt:   rfc3339Ptr(row.NextDueAt),
	}
}

func cronFire(ctx context.Context, st *store.Store, fire store.CronFire) cronFireDTO {
	return cronFireDTO{
		ID:         fire.ID,
		ScheduleID: fire.ScheduleID,
		DueAt:      rfc3339(fire.DueAt),
		DecidedAt:  rfc3339(fire.DecidedAt),
		Outcome:    fire.Outcome,
		RunID:      fire.RunID,
		Detail:     fire.Detail,
		RunStatus:  cronRunStatus(ctx, st, fire.RunID),
	}
}

// safety: a run the store no longer holds reads as no status, not an error; the fire is still history.
func cronRunStatus(ctx context.Context, st *store.Store, runID string) string {
	if runID == "" || st == nil {
		return ""
	}
	run, err := st.GetRun(ctx, runID)
	if err != nil || run == nil {
		return ""
	}
	return run.Status
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := rfc3339(*t)
	return &s
}
