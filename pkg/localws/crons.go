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

// safety: the same directory `sparkwing crons` pins into, so the dashboard
// reads a lock the same way the CLI wrote it.
const cronPinDir = "crons"

// safety: the launch persists a trigger and may start a consumer, so it needs longer than a read.
const cronRunTimeout = 60 * time.Second

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
	out := crons.OverviewView{Health: crons.NewHealthView(health), Schedules: make([]crons.ScheduleView, 0, len(rows))}
	for _, row := range rows {
		out.Schedules = append(out.Schedules, crons.NewScheduleView(row))
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
	out := crons.DetailView{
		Schedule: crons.NewScheduleView(row),
		Fires:    make([]crons.FireView, 0, len(fires)),
		Upcoming: make([]string, 0, len(upcoming)),
	}
	for _, fire := range fires {
		out.Fires = append(out.Fires, crons.NewFireView(fire, cronRunStatus(ctx, a.store, fire.RunID)))
	}
	for _, at := range upcoming {
		out.Upcoming = append(out.Upcoming, crons.RFC3339(at))
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
	writeJSON(w, crons.ScheduleEnvelope{Schedule: crons.NewScheduleView(row)})
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
	writeJSON(w, crons.RunEnvelope{RunID: runID, Schedule: crons.NewScheduleView(row)})
}

// safety: the dashboard never ticks and never launches in process, so this service carries
// neither a lock path nor a launcher.
func (a *cronsAPI) service(w http.ResponseWriter) (*crons.Service, bool) {
	if a.store == nil {
		http.Error(w, "this dashboard has no local runs store, so it cannot read schedules",
			http.StatusBadGateway)
		return nil, false
	}
	return &crons.Service{Store: a.store, PinRoot: filepath.Join(a.paths.Root, cronPinDir)}, true
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

