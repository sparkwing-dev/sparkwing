package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// LocalScheduleProver compiles selected checkout code with captured host inputs.
type LocalScheduleProver func(context.Context, string, string, []string) (crons.Proof, error)

// LocalScheduleEnvironments retains and verifies private schedule inputs.
type LocalScheduleEnvironments interface {
	Capture(context.Context, []string) (runspec.EnvSnapshot, error)
	Load(context.Context, runspec.EnvSnapshot) ([]string, error)
}

// WithLocalSchedules binds local routes to the elected store and host environment owner.
func (s *Server) WithLocalSchedules(service *crons.Service, prove LocalScheduleProver, environments LocalScheduleEnvironments) *Server {
	s.localSchedules, s.localScheduleProver, s.localScheduleEnvironments = service, prove, environments
	return s
}

// ScheduleLauncher supplies the controller's shared scheduled-run launcher.
func (s *Server) ScheduleLauncher() crons.Launcher { return cronLauncher{server: s} }

func (s *Server) localScheduleService(r *http.Request, a capability.Authorized) (*crons.Service, error) {
	_, peer := wingd.LocalPeer(r.Context())
	if !peer || a.Principal.Capability.Subject.Kind != capability.SubjectLocal || a.Target.Team != string(store.DefaultTeam) {
		return nil, &apierr.Error{Code: apierr.Forbidden, Message: "schedule operation requires the verified local peer"}
	}
	if s.localSchedules == nil || s.localSchedules.Operator == nil || s.operator == nil || *s.localSchedules.Operator != *s.operator || s.localScheduleEnvironments == nil || s.localScheduleProver == nil {
		return nil, &apierr.Error{Code: apierr.Unavailable, Message: "local schedule service is unavailable"}
	}
	return s.localSchedules, nil
}

func (s *Server) handleRegisterLocalSchedules(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	service, err := s.localScheduleService(r, a)
	if err != nil {
		return err
	}
	var input controllerwire.LocalScheduleRegistration
	if err := decodeEngineRequest(r, &input); err != nil {
		return err
	}
	report, err := service.RegisterLocal(r.Context(), input, func(ctx context.Context, root, pipeline string) (crons.Proof, error) {
		return s.localScheduleProver(ctx, root, pipeline, input.Environment)
	}, s.localScheduleEnvironments.Capture, func(ctx context.Context, rows []store.CronSchedule, registration crons.LocalRegistration, replace bool) (crons.ArmReport, error) {
		return s.commitLocalSchedules(ctx, a, input.RepoPath, rows, registration, replace)
	})
	if err != nil {
		return engineLookupError(err)
	}
	result := controllerwire.ScheduleRepoWritten{Schedules: []controllerwire.ScheduleView{}, Withdrawn: report.Withdrawals}
	for _, row := range report.Schedules {
		view, err := s.scheduleView(r, a, row.ID)
		if err != nil {
			return err
		}
		result.Schedules = append(result.Schedules, view)
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func (s *Server) handlePinLocalSchedule(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	service, err := s.localScheduleService(r, a)
	if err != nil {
		return err
	}
	var view controllerwire.ScheduleView
	err = service.WithLocalLock(r.Context(), func() error {
		row, err := service.Resolve(r.Context(), r.PathValue("schedule"))
		if err != nil {
			return err
		}
		if row.Where != store.CronWhereLocal {
			return invalidEngineBody("only checkout schedules can be pinned locally")
		}
		if strings.HasSuffix(r.URL.Path, "/unpin") {
			_, err = service.Unlock(r.Context(), row.ID)
		} else {
			registration, readErr := crons.ReadLocalRegistration(r.Context(), filepath.Dir(service.PinRoot), row.ID)
			if readErr != nil {
				return readErr
			}
			environment, readErr := s.localScheduleEnvironments.Load(r.Context(), registration.Environment)
			if readErr != nil {
				return readErr
			}
			_, err = service.Lock(r.Context(), row.ID, func(ctx context.Context, root, pipeline string) (crons.Proof, error) {
				return s.localScheduleProver(ctx, root, pipeline, environment)
			})
		}
		if err != nil {
			return err
		}
		view, err = s.scheduleView(r, a, row.ID)
		return err
	})
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.ScheduleEnvelope{Schedule: view})
	return nil
}

func (s *Server) handleTickLocalSchedules(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	service, err := s.localScheduleService(r, a)
	if err != nil {
		return err
	}
	var input struct {
		DryRun bool `json:"dry_run"`
	}
	if err := decodeEngineRequest(r, &input); err != nil {
		return err
	}
	report, err := service.Tick(r.Context(), input.DryRun)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, struct {
		Tick controllerwire.TickView `json:"tick"`
	}{controllerwire.TickView{At: crons.RFC3339(report.At), Host: service.Host, Version: service.Version, Error: strings.Join(report.Errors, "; ")}})
	return nil
}
