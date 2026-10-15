package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runflags"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var gitObjectSHA = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var problems []string

	authState := "disabled"
	if s.auth != nil {
		authState = "enabled"
	}

	if err := s.store.DB().PingContext(r.Context()); err != nil {
		s.logger.Error("controller health database check failed", "err", err)
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
	if sha == "" || gitObjectSHA.MatchString(sha) {
		return nil
	}
	return errors.New("git.sha must be a 40-64 character hex object id")
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request, authorized capability.Authorized) error {
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
	runs = store.RedactedRuns(runs)
	if runs == nil {
		runs = []*store.Run{}
	}
	w.Header().Set("X-Sparkwing-Run-Filter-Version", store.RunFilterVersion)
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	return nil
}

func includeHas(csv, target string) bool {
	for _, p := range strings.Split(csv, ",") {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
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
	writeJSON(w, http.StatusOK, store.RedactedRun(run))
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

type triggerReqGit struct {
	Branch      string `json:"branch,omitempty"`
	SHA         string `json:"sha,omitempty"`
	Repo        string `json:"repo,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	GithubOwner string `json:"github_owner,omitempty"`
	GithubRepo  string `json:"github_repo,omitempty"`
}

// safety: Env arrives sanitized; a caller inside the process supplies only keys
// a run may read, because nothing filters it again here.
type triggerIntake struct {
	RunID         string
	Pipeline      string
	Args          map[string]string
	Source        string
	User          string
	Env           map[string]string
	Git           triggerReqGit
	ParentRunID   string
	ParentNodeID  string
	RetryOf       string
	RepoInherited bool
	// safety: a second intake under one key fails with
	// store.ErrDuplicateIdempotencyKey rather than starting a second run.
	IdempotencyKey string
	At             time.Time
	SourceKey      string
	SourceToken    string
}

// safety: every path that starts a run on this controller writes its three rows
// here -- the trigger, the pending run, the dispatch -- so an HTTP submission
// and a schedule the controller fired land identically.
func (s *Server) admitTrigger(ctx context.Context, t *store.Tenant, in triggerIntake) error {
	// safety: the trigger and the run it names are written together, so a guard
	// that refuses the run leaves no trigger behind for a worker to claim.
	trig, run := triggerRows(in)
	var err error
	if in.SourceKey != "" {
		err = t.CreateSourceTriggerWithRun(ctx, trig, run, in.SourceKey, in.User, in.SourceToken)
	} else {
		err = t.CreateTriggerWithRun(ctx, trig, run)
	}
	if err != nil {
		if errors.Is(err, store.ErrDuplicateIdempotencyKey) || errors.Is(err, store.ErrComputeLimit) {
			return err
		}
		if errors.Is(err, store.ErrSourceAlreadyBound) {
			return err
		}
		return fmt.Errorf("persist the trigger and its run: %w", err)
	}

	s.recordQueueActivity(in.At)
	return nil
}

func triggerRows(in triggerIntake) (store.Trigger, store.Run) {
	trig := store.Trigger{
		ID:             in.RunID,
		Pipeline:       in.Pipeline,
		Args:           in.Args,
		TriggerSource:  in.Source,
		TriggerUser:    in.User,
		TriggerEnv:     in.Env,
		GitBranch:      in.Git.Branch,
		GitSHA:         in.Git.SHA,
		Repo:           in.Git.Repo,
		RepoURL:        in.Git.RepoURL,
		GithubOwner:    in.Git.GithubOwner,
		GithubRepo:     in.Git.GithubRepo,
		CreatedAt:      in.At,
		ParentRunID:    in.ParentRunID,
		ParentNodeID:   in.ParentNodeID,
		RetryOf:        in.RetryOf,
		RepoInherited:  in.RepoInherited,
		IdempotencyKey: in.IdempotencyKey,
	}
	run := store.Run{
		ID:            in.RunID,
		Pipeline:      in.Pipeline,
		Status:        "pending",
		TriggerSource: in.Source,
		GitBranch:     in.Git.Branch,
		GitSHA:        in.Git.SHA,
		Args:          in.Args,
		ParentRunID:   in.ParentRunID,
		DeclaredRepo:  in.Git.Repo,
		RepoURL:       in.Git.RepoURL,
		GithubOwner:   in.Git.GithubOwner,
		GithubRepo:    in.Git.GithubRepo,
		RetryOf:       in.RetryOf,
		CreatedAt:     in.At,
		StartedAt:     in.At,
		Invocation:    triggerRunInvocation(in.Env),
	}
	return trig, run
}

// safety: every host of a triggered run, the planning claim's included, reads the trigger's run flags and the
// default branch a GitHub App trigger recorded from the run row, so every intake that creates a run records them.
func triggerRunInvocation(env map[string]string) map[string]any {
	inv := runflags.FromTriggerEnv(env).Invocation()
	if branch := env[EnvDefaultBranch]; branch != "" {
		if inv == nil {
			inv = map[string]any{}
		}
		inv[runflags.DefaultBranchKey] = branch
	}
	return inv
}

func (s *Server) handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	localDir := s.dashboard != nil && s.dashboard.Local && s.dashboard.Paths.Root != ""
	// safety: a laptop run's directory is named by its id, so an id that is not one path segment is refused
	// before anything is deleted rather than leaving its row gone and its directory behind.
	if err := storage.SafeSegment(id); localDir && err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// safety: the run's log records are the only index of its segments, so
	// the segments go first.
	if _, err := s.deleteDataLogs(r.Context(), tenant.Team(), id); err != nil {
		s.writeInternalError(w, r, "delete the run's log segments", err)
		return
	}
	if err := tenant.DeleteRun(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if localDir {
		if err := removeLocalRunDir(s.dashboard.Paths.RunsDir(), id); err != nil {
			s.writeInternalError(w, r, "remove the run's directory", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
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

type claimResources struct {
	Capacity     match.Resources `json:"capacity"`
	Availability match.Resources `json:"availability"`
}

func newRunID() string {
	ts := time.Now().UTC().Format("20060102-150405")
	var suffix [2]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("run-%s-%s", ts, hex.EncodeToString(suffix[:]))
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

// safety: a chunked body reports no content length, so a route whose body is
// optional reads it whenever there is one and treats only an empty body as
// absent; gating on a positive length dropped what a streaming client sent.
func decodeOptionalJSON(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if err := decodeJSON(r, v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
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
		message = "internal server error"
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) writeInternalError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	s.logger.Error(operation, append(requestLogAttrs(r), "err", err)...)
	writeError(w, http.StatusInternalServerError, err)
}

type claimCapacity struct {
	MaxConcurrent int `json:"max_concurrent"`
	ActiveClaims  int `json:"active_claims"`
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
	if steps == nil {
		steps = []*store.NodeStep{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"steps": steps})
	return nil
}

func (s *Server) handleCreateDebugPause(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	var body store.DebugPause
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	body.RunID = runID
	if body.NodeID == "" || body.Reason == "" {
		writeError(w, http.StatusBadRequest, errors.New("node_id and reason are required"))
		return
	}
	tenant, ok := s.requestTenant(w, r)
	if !ok {
		return
	}
	if err := tenant.CreateDebugPause(r.Context(), body); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
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
	writeJSON(w, http.StatusOK, api.PublicEvents(events))
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
	if pauses == nil {
		pauses = []*store.DebugPause{}
	}
	writeJSON(w, http.StatusOK, pauses)
	return nil
}

func auditPrincipal(r *http.Request) string {
	if p, ok := PrincipalFromContext(r.Context()); ok && p != nil && p.Name != "" {
		return p.Name
	}
	return "anonymous"
}
