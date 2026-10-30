package controller

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/cronspec"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// scheduleWriter rechecks authority and repository policy in each atomic schedule or intake transaction.
type scheduleWriter interface {
	Team() store.Team
	ReplaceAuthorizedSchedules(context.Context, capability.Authorized, store.ScheduleRepoRequest) ([]string, []string, error)
	DeleteAuthorizedSchedules(context.Context, capability.Authorized, string) (int, error)
	PauseAuthorizedSchedule(context.Context, capability.Authorized, string, bool) (string, error)
	OverrideAuthorizedSchedule(context.Context, capability.Authorized, string, *cronspec.Override) (string, error)
	RunAuthorizedSchedule(context.Context, capability.Authorized, string, string) (string, string, bool, error)
	DeleteAuthorizedSchedule(context.Context, capability.Authorized, string) (string, bool, error)
}

func (s *Server) scheduleWriter(r *http.Request, a capability.Authorized) (scheduleWriter, error) {
	return tenantPort[scheduleWriter](r.Context(), s, store.Team(a.Target.Team))
}

func (s *Server) scheduleEndpoints() []controllerwire.Endpoint {
	return []controllerwire.Endpoint{
		{Method: "PUT", Pattern: "/api/v1/schedules/local/repos", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRegisterLocalSchedules},
		{Method: "POST", Pattern: "/api/v1/schedules/local/tick", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleTickLocalSchedules},
		{Method: "POST", Pattern: "/api/v1/schedules/{schedule}/pin", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePinLocalSchedule},
		{Method: "POST", Pattern: "/api/v1/schedules/{schedule}/unpin", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePinLocalSchedule},
		{Method: "PUT", Pattern: "/api/v1/schedules/repos", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePutCronRepo},
		{Method: "DELETE", Pattern: "/api/v1/schedules/repos", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDeleteCronRepo},
		{Method: "POST", Pattern: "/api/v1/schedules/{schedule}/pause", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePauseSchedule},
		{Method: "POST", Pattern: "/api/v1/schedules/{schedule}/resume", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handlePauseSchedule},
		{Method: "POST", Pattern: "/api/v1/schedules/{schedule}/run", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleRunCronNow},
		{Method: "DELETE", Pattern: "/api/v1/schedules/{schedule}", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleDisarmCron},
		{Method: "PUT", Pattern: "/api/v1/schedules/{schedule}/override", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleScheduleOverride},
		{Method: "DELETE", Pattern: "/api/v1/schedules/{schedule}/override", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.handleScheduleOverride},
	}
}

func (s *Server) handlePutCronRepo(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body controllerwire.ScheduleRepoRequest
	if err := decodeEngineRequest(r, &body); err != nil {
		return err
	}
	if body.Mode == "" {
		body.Mode = controllerwire.ScheduleReplace
	}
	repo, err := sourceurl.ValidateCloneURL(body.RepoURL)
	if err != nil {
		return invalidEngineBody("invalid repository URL")
	}
	body.RepoURL = repo
	if err := validateGitBranchName(body.Branch); err != nil {
		return invalidEngineBody("invalid branch")
	}
	if body.Follow {
		body.SHA = ""
	}
	if body.SHA != "" {
		if err := validateGitSHA(body.SHA); err != nil {
			return invalidEngineBody("invalid commit SHA")
		}
	}
	entries := make([]cronRepoSchedule, 0, len(body.Schedules))
	for _, entry := range body.Schedules {
		entries = append(entries, cronRepoSchedule(entry))
	}
	if _, err := declaredFromRequest(entries); err != nil {
		return invalidEngineBody("invalid schedule declaration")
	}
	writer, err := s.scheduleWriter(r, a)
	if err != nil {
		return err
	}
	ids, withdrawn, err := writer.ReplaceAuthorizedSchedules(r.Context(), a, scheduleRequestForStore(body))
	if err != nil {
		if s.writeComputeLimitRefusal(w, r, err) {
			return nil
		}
		return engineLookupError(err)
	}
	out := controllerwire.ScheduleRepoWritten{Schedules: []controllerwire.ScheduleView{}, Withdrawn: withdrawn}
	for _, id := range ids {
		view, err := s.scheduleView(r, a, id)
		if err != nil {
			return err
		}
		out.Schedules = append(out.Schedules, view)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleDeleteCronRepo(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	repo, err := sourceurl.ValidateCloneURL(r.URL.Query().Get("repo_url"))
	if err != nil {
		return invalidEngineBody("invalid repository URL")
	}
	writer, err := s.scheduleWriter(r, a)
	if err != nil {
		return err
	}
	removed, err := writer.DeleteAuthorizedSchedules(r.Context(), a, repo)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.ScheduleRepoDeletion{RepoURL: repo, Removed: removed})
	return nil
}

func (s *Server) handlePauseSchedule(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.scheduleWriter(r, a)
	if err != nil {
		return err
	}
	var id string
	err = s.withScheduleLock(r, a, func() error {
		var err error
		id, err = writer.PauseAuthorizedSchedule(r.Context(), a, r.PathValue("schedule"), r.Pattern == "POST /api/v1/schedules/{schedule}/pause")
		return err
	})
	if err != nil {
		return engineLookupError(err)
	}
	return s.writeScheduleView(w, r, a, id)
}

func (s *Server) handleScheduleOverride(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	var body *cronspec.Override
	if r.Method == http.MethodPut {
		body = &cronspec.Override{}
		if err := decodeEngineRequest(r, body); err != nil {
			return err
		}
		if err := body.Validate(); err != nil {
			return invalidEngineBody(err.Error())
		}
	}

	writer, err := s.scheduleWriter(r, a)
	if err != nil {
		return err
	}
	var id string
	err = s.withScheduleLock(r, a, func() error {
		var err error
		id, err = writer.OverrideAuthorizedSchedule(r.Context(), a, r.PathValue("schedule"), body)
		return err
	})
	if err != nil {
		return engineLookupError(err)
	}
	return s.writeScheduleView(w, r, a, id)
}

func (s *Server) handleRunCronNow(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	key, err := controlIdempotencyKey(r)
	if err != nil {
		return err
	}
	writer, err := s.scheduleWriter(r, a)
	if err != nil {
		return err
	}
	id, run, replayed, err := writer.RunAuthorizedSchedule(r.Context(), a, r.PathValue("schedule"), key)
	if err != nil {
		return engineLookupError(err)
	}
	view, err := s.scheduleView(r, a, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, controllerwire.ScheduleRun{RunID: run, Schedule: view, Replayed: replayed})
	return nil
}

func (s *Server) handleDisarmCron(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	writer, err := s.scheduleWriter(r, a)
	if err != nil {
		return err
	}
	var id string
	var deleted bool
	err = s.withScheduleLock(r, a, func() error {
		var err error
		id, deleted, err = writer.DeleteAuthorizedSchedule(r.Context(), a, r.PathValue("schedule"))
		if err == nil && deleted && s.localSchedules != nil {
			err = os.RemoveAll(filepath.Join(s.localSchedules.PinRoot, id))
		}
		return err
	})
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, controllerwire.ScheduleDeletion{ScheduleID: id, Deleted: deleted})
	return nil
}

func (s *Server) scheduleView(r *http.Request, a capability.Authorized, id string) (controllerwire.ScheduleView, error) {
	tenant, err := s.reportingTenant(r, a)
	if err != nil {
		return controllerwire.ScheduleView{}, engineLookupError(err)
	}
	row, _, err := s.cronServiceFor(tenant, "").Show(r.Context(), id, 1)
	return crons.NewScheduleView(row), engineLookupError(err)
}

func (s *Server) writeScheduleView(w http.ResponseWriter, r *http.Request, a capability.Authorized, id string) error {
	view, err := s.scheduleView(r, a, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, controllerwire.ScheduleEnvelope{Schedule: view})
	return nil
}

func scheduleCount(r *http.Request, name string, fallback int) (int, error) {
	values, exists := r.URL.Query()[name]
	if !exists {
		return fallback, nil
	}
	if len(values) != 1 {
		return 0, invalidEngineBody("one " + name + " count is required")
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value < 0 {
		return 0, invalidEngineBody(name + " count must be nonnegative")
	}
	return value, nil
}

func (s *Server) withScheduleLock(r *http.Request, a capability.Authorized, edit func() error) error {
	if s.localSchedules == nil {
		return edit()
	}
	service, err := s.localScheduleService(r, a)
	if err != nil {
		return err
	}
	return service.WithLocalLock(r.Context(), edit)
}

func scheduleRequestForStore(body controllerwire.ScheduleRepoRequest) store.ScheduleRepoRequest {
	return store.ScheduleRepoRequest{Mode: store.ScheduleWriteMode(body.Mode), RepoURL: body.RepoURL, Branch: body.Branch, SHA: body.SHA, Follow: body.Follow, Schedules: body.Schedules}
}
