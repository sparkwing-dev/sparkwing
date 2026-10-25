package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var problems []string

	authState := "enabled"

	if err := s.operator.Ping(r.Context()); err != nil {
		apierr.NoteResponseError(w, fmt.Errorf("controller health database check: %w", err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":   "degraded",
			"auth":     authState,
			"problems": []string{"db: unavailable"},
		})
		return
	}

	objectStore, objectStoreProblems := objectStoreHealth(s.bucketMeasured())
	problems = append(problems, objectStoreProblems...)

	egressState, egressProblems := s.egressHealth()
	problems = append(problems, egressProblems...)

	if problem := s.deployment.LicenseProblem(time.Now()); problem != "" {
		problems = append(problems, problem)
	}

	if s.githubAppProblem != nil {
		problems = append(problems, errGitHubAppUnavailable.Error())
	}
	if s.hasPreviousWebhookSecret() {
		problems = append(problems, previousWebhookSecretWarning)
	}

	resp := map[string]any{
		"status": "ok", "auth": authState,
		"object_store": objectStore, "database": s.storageHealth(),
		"egress": egressState,
	}
	if len(problems) > 0 {
		resp["status"] = "degraded"
		resp["problems"] = problems
	}
	writeJSON(w, http.StatusOK, resp)
}

// safety: a stored sha reaches a runner's git as a revision argument, so only an object id may pass.
func validateGitSHA(sha string) error {
	if sha == "" || runspec.IsCommitID(sha) {
		return nil
	}
	return errors.New("git.sha must be a 40- or 64-character hex object id")
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	if r.Header.Get("Accept") == "application/json" {
		return s.handleRunPage(w, r, authorized)
	}
	filter, parseErr := store.ParseRunFilterValidated(r.URL.Query())
	if parseErr != nil {
		return invalidEngineBody(parseErr.Error())
	}
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runs, err := tenant.ListRuns(r.Context(), filter)
	if err != nil {
		return engineLookupError(err)
	}

	out := make([]runstate.RunSummary, len(runs))
	for i, row := range runs {
		out[i] = row.RunSummary
	}
	w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
	return nil
}

func (s *Server) handlePipelineLatest(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	name := r.PathValue("name")
	if name == "" {
		return invalidEngineBody("pipeline name required")
	}
	q := r.URL.Query()
	statuses := splitCSV(q.Get("status"))
	if len(statuses) == 0 {
		statuses = []string{"success"}
	}
	var maxAge time.Duration
	if v := q.Get("max_age"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return invalidEngineBody("invalid max_age")
		}
		if d < 0 {
			return invalidEngineBody("max_age must be >= 0")
		}
		maxAge = d
	}
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	run, err := tenant.GetLatestRun(r.Context(), name, statuses, maxAge)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, run.RunSummary)
	return nil
}

func splitCSV(s string) []string {
	raw := strings.Split(s, ",")
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request, a capability.Authorized) error {
	id := a.Target.RunID
	localDir := s.dashboard != nil && s.dashboard.Local && s.dashboard.Paths.Root != ""
	if err := storage.SafeSegment(id); localDir && err != nil {
		return invalidEngineBody(err.Error())
	}
	writer, err := s.teamWriter(r, a)
	if err != nil {
		return err
	}
	if err := writer.DeleteAuthorizedRun(r.Context(), a); err != nil {
		return engineLookupError(err)
	}
	if _, err := s.deleteDataLogs(r.Context(), store.Team(a.Target.Team), id); err != nil {
		return err
	}
	if localDir {
		if err := removeLocalRunDir(s.dashboard.Paths.RunsDir(), id); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// safety: a laptop dashboard's node logs live in the run's directory on its own disk; os.Root keeps a planted
// symlink from turning the removal into one outside the runs tree.
func removeLocalRunDir(runsDir, runID string) error {
	root, err := os.OpenRoot(runsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(root.RemoveAll(runID), root.Close())
}

const (
	maxJSONBody = 1 << 20
	// safety: a secret value is caller data, so it gets its own ceiling
	// rather than an exemption from the shared decode path.
	maxSecretJSONBody = 8 << 20
	// safety: a plan snapshot is stored verbatim, so decoding does not bound its size.
	maxPlanSnapshotBody = store.MaxPlanBytes
)

func decodeJSON(r *http.Request, v any) error {
	return decodeJSONLimit(r, v, maxJSONBody)
}

func decodeJSONLimit(r *http.Request, v any, limit int64) error {
	defer r.Body.Close()
	// safety: an application/json body forces a CORS preflight, so a page
	// on another site cannot post one as a simple request.
	if err := requireJSONContentType(r.Header.Get("Content-Type")); err != nil {
		return err
	}
	body := http.MaxBytesReader(nil, r.Body, limit)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

func requireJSONContentType(header string) error {
	if header == "" {
		return errors.New("content-type application/json required")
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return fmt.Errorf("content-type %q: %w", header, err)
	}
	if mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json") {
		return fmt.Errorf("content-type %q: application/json required", mediaType)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, store.ErrLockHeld) {
		status = http.StatusConflict
	}
	message := err.Error()
	if status >= http.StatusInternalServerError {
		apierr.NoteResponseError(w, err)
		message = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) writeInternalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	err = fmt.Errorf("%s: %w", operation, err)
	writeError(w, http.StatusInternalServerError, err)
}

func (s *Server) handleListNodeSteps(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runID := authorized.Target.RunID
	steps, err := tenant.ListNodeSteps(r.Context(), runID)
	if err != nil {
		return engineLookupError(err)
	}
	writeJSON(w, http.StatusOK, publicRunSteps(steps))
	return nil
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	runID := authorized.Target.RunID
	var afterSeq int64
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return invalidEngineBody("invalid after")
		}
		afterSeq = n
	}
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return invalidEngineBody("invalid limit")
		}
		limit = min(n, store.MaxRunListLimit)
	}
	events, err := tenant.ListEventsAfter(r.Context(), runID, afterSeq, limit)
	if err != nil {
		return engineLookupError(err)
	}
	if events == nil {
		events = []store.Event{}
	}
	public := api.PublicEvents(events)
	out := make([]controllerwire.RunEvent, len(public))
	for i, event := range public {
		out[i] = controllerwire.RunEvent(event)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleListDebugPauses(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
	runID := authorized.Target.RunID
	tenant, err := s.reportingTenant(r, authorized)
	if err != nil {
		return engineLookupError(err)
	}
	pauses, err := tenant.ListDebugPauses(r.Context(), runID)
	if err != nil {
		return engineLookupError(err)
	}
	out := make([]controllerwire.PauseHistory, len(pauses))
	for i, row := range pauses {
		out[i] = controllerwire.PauseHistory(*row)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
