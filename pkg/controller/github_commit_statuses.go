package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	githubAPIBaseURL        = "https://api.github.com"
	githubStatusAPIVersion  = "2022-11-28"
	githubStatusTimeout     = 10 * time.Second
	githubStatusQueueSize   = 64
	githubStatusHistorySize = 4096
)

type githubCommitStatusReporter struct {
	token        string
	dashboardURL string
	apiBaseURL   string
	httpClient   *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	wake         chan struct{}
	done         chan struct{}
	mu           sync.Mutex
	accepting    bool
	capacity     int
	historyLimit int
	slots        map[githubCommitStatusKey]*githubCommitStatusSlot
	generations  map[githubCommitStatusTarget]githubCommitStatusGeneration
	sequence     uint64
	touched      uint64
}

type githubCommitStatusJob struct {
	key    githubCommitStatusKey
	logger *slog.Logger
	status githubCommitStatus
}

type githubCommitStatusKey struct {
	runID    string
	pipeline string
}

type githubCommitStatusSlot struct {
	target           githubCommitStatusTarget
	generation       uint64
	pending          *githubCommitStatusJob
	terminal         *githubCommitStatusJob
	unresolved       bool
	inFlight         bool
	awaitingTerminal bool
}

type githubCommitStatusTarget struct {
	owner   string
	repo    string
	sha     string
	context string
}

type githubCommitStatusGeneration struct {
	runID      string
	generation uint64
	touched    uint64
}

type githubCommitStatusRequest struct {
	State       string `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description"`
	Context     string `json:"context"`
}

type githubCommitStatus struct {
	Owner       string
	Repo        string
	SHA         string
	Pipeline    string
	RunID       string
	State       string
	Description string
}

// WithGitHubCommitStatuses enables best-effort GitHub commit statuses for
// pull_request webhook runs. token authenticates requests to GitHub; an empty
// token disables reporting. dashboardURL optionally supplies the dashboard
// base URL used for each status's run-detail link. The dashboard URL must be
// HTTP(S), include a host, and omit credentials, query, and fragment. For one
// commit and pipeline, a newer accepted run suppresses older terminal updates.
func (s *Server) WithGitHubCommitStatuses(token, dashboardURL string) *Server {
	if s.githubCommitStatuses != nil {
		s.githubCommitStatuses.stop()
		s.githubCommitStatuses = nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return s
	}
	s.githubCommitStatuses = newGitHubCommitStatusReporter(
		token,
		strings.TrimSpace(dashboardURL),
		githubAPIBaseURL,
		&http.Client{Timeout: githubStatusTimeout},
	)
	return s
}

func newGitHubCommitStatusReporter(token, dashboardURL, apiBaseURL string, httpClient *http.Client) *githubCommitStatusReporter {
	return newGitHubCommitStatusReporterWithCapacity(
		token, dashboardURL, apiBaseURL, httpClient, githubStatusQueueSize,
	)
}

func newGitHubCommitStatusReporterWithCapacity(token, dashboardURL, apiBaseURL string, httpClient *http.Client, capacity int) *githubCommitStatusReporter {
	ctx, cancel := context.WithCancel(context.Background())
	r := &githubCommitStatusReporter{
		token:        token,
		dashboardURL: dashboardURL,
		apiBaseURL:   strings.TrimRight(apiBaseURL, "/"),
		httpClient:   httpClient,
		ctx:          ctx,
		cancel:       cancel,
		wake:         make(chan struct{}, 1),
		done:         make(chan struct{}),
		accepting:    true,
		capacity:     capacity,
		historyLimit: githubStatusHistorySize,
		slots:        make(map[githubCommitStatusKey]*githubCommitStatusSlot),
		generations:  make(map[githubCommitStatusTarget]githubCommitStatusGeneration),
	}
	go r.run()
	return r
}

func (s *Server) reportGitHubCommitStatus(ctx context.Context, runID, runStatus string) {
	reporter, status, ok := s.githubCommitStatus(ctx, runID, runStatus)
	if !ok {
		return
	}
	reporter.enqueue(s.logger, status)
}

func (s *Server) reserveGitHubCommitStatus(ctx context.Context, runID, runStatus string) func(bool) {
	reporter, status, ok := s.githubCommitStatus(ctx, runID, runStatus)
	if !ok {
		return func(bool) {}
	}
	return reporter.reserve(s.logger, status)
}

func (s *Server) githubCommitStatus(ctx context.Context, runID, runStatus string) (*githubCommitStatusReporter, githubCommitStatus, bool) {
	reporter := s.githubCommitStatuses
	if reporter == nil {
		return nil, githubCommitStatus{}, false
	}
	trigger, err := s.store.GetTrigger(ctx, runID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, githubCommitStatus{}, false
	}
	if err != nil {
		s.logger.Warn("github commit status trigger lookup failed", "run_id", runID, "err", err)
		return nil, githubCommitStatus{}, false
	}
	status, ok := githubCommitStatusFromTrigger(trigger, runStatus)
	if !ok {
		return nil, githubCommitStatus{}, false
	}
	return reporter, status, true
}

func (r *githubCommitStatusReporter) enqueue(logger *slog.Logger, status githubCommitStatus) bool {
	key := githubCommitStatusKey{runID: status.RunID, pipeline: status.Pipeline}
	target := githubCommitStatusTargetFor(status)
	r.mu.Lock()
	slot := r.slots[key]
	accepted := false
	reason := "capacity"
	if r.accepting && status.State == "pending" {
		if slot == nil && len(r.slots) < r.capacity {
			slot = &githubCommitStatusSlot{target: target}
			r.slots[key] = slot
		}
		if slot != nil && slot.target == target {
			if slot.generation == 0 {
				slot.generation = r.nextGenerationLocked()
			}
			job := &githubCommitStatusJob{
				key: key, logger: logger, status: status,
			}
			if r.activateGenerationLocked(key, slot) {
				slot.pending = job
				slot.awaitingTerminal = slot.terminal == nil
				accepted = true
			} else {
				reason = "superseded"
			}
		}
	} else if r.accepting {
		reason = "stale"
		current, tracked := r.generations[target]
		if tracked && current.runID != status.RunID {
			reason = "superseded"
		}
		if slot != nil && slot.target == target {
			job := &githubCommitStatusJob{
				key: key, logger: logger, status: status,
			}
			if slot.unresolved && (!tracked || current.generation <= slot.generation) {
				slot.terminal = job
				slot.awaitingTerminal = false
				accepted = true
			} else if tracked && current.runID == status.RunID && current.generation == slot.generation {
				r.touchGenerationLocked(target)
				slot.terminal = job
				slot.awaitingTerminal = false
				accepted = true
			} else if tracked {
				reason = "superseded"
			}
		}
	}
	if accepted {
		r.signalLocked()
	} else if slot != nil && !slot.inFlight && !slot.unresolved && slot.pending == nil && slot.terminal == nil {
		delete(r.slots, key)
	}
	r.mu.Unlock()
	if !accepted {
		logGitHubCommitStatusDrop(logger, status, reason)
	}
	return accepted
}

func (r *githubCommitStatusReporter) reserve(logger *slog.Logger, status githubCommitStatus) func(bool) {
	key := githubCommitStatusKey{runID: status.RunID, pipeline: status.Pipeline}
	targetKey := githubCommitStatusTargetFor(status)
	r.mu.Lock()
	slot := r.slots[key]
	if slot == nil && r.accepting && len(r.slots) < r.capacity {
		slot = &githubCommitStatusSlot{target: targetKey, generation: r.nextGenerationLocked()}
		r.slots[key] = slot
	}
	accepted := r.accepting && slot != nil && slot.target == targetKey
	if accepted {
		if slot.generation == 0 {
			slot.generation = r.nextGenerationLocked()
		}
		slot.pending = &githubCommitStatusJob{
			key: key, logger: logger, status: status,
		}
		slot.unresolved = true
		r.signalLocked()
	}
	r.mu.Unlock()
	if !accepted {
		logGitHubCommitStatusDrop(logger, status, "capacity")
	}
	var once sync.Once
	return func(dispatchAccepted bool) {
		once.Do(func() {
			if accepted {
				r.resolve(key, slot, dispatchAccepted)
			}
		})
	}
}

func (r *githubCommitStatusReporter) resolve(key githubCommitStatusKey, target *githubCommitStatusSlot, accepted bool) {
	var dropped *githubCommitStatusJob
	r.mu.Lock()
	slot := r.slots[key]
	if slot == target {
		slot.unresolved = false
		if !accepted {
			slot.pending = nil
			slot.awaitingTerminal = false
			if current, ok := r.generations[slot.target]; ok && current.runID != key.runID {
				slot.terminal = nil
			}
		} else if !r.activateGenerationLocked(key, slot) {
			dropped = slot.pending
			slot.pending = nil
			slot.terminal = nil
			slot.awaitingTerminal = false
		} else {
			slot.awaitingTerminal = slot.terminal == nil
		}
		if !slot.inFlight && slot.pending == nil && slot.terminal == nil && !slot.awaitingTerminal {
			delete(r.slots, key)
		}
		r.signalLocked()
	}
	r.mu.Unlock()
	if dropped != nil {
		logGitHubCommitStatusDrop(dropped.logger, dropped.status, "superseded")
	}
}

func (r *githubCommitStatusReporter) nextGenerationLocked() uint64 {
	r.sequence++
	return r.sequence
}

func (r *githubCommitStatusReporter) activateGenerationLocked(key githubCommitStatusKey, slot *githubCommitStatusSlot) bool {
	current, ok := r.generations[slot.target]
	if ok && current.runID != key.runID && current.generation > slot.generation {
		return false
	}
	if !ok || current.runID != key.runID || current.generation < slot.generation {
		if !r.rememberGenerationLocked(slot.target, key.runID, slot.generation) {
			return false
		}
		for otherKey, other := range r.slots {
			if otherKey == key || other.target != slot.target || other.generation >= slot.generation {
				continue
			}
			other.pending = nil
			other.terminal = nil
			other.awaitingTerminal = false
			if !other.inFlight && !other.unresolved {
				delete(r.slots, otherKey)
			}
		}
	} else {
		r.touchGenerationLocked(slot.target)
	}
	return true
}

func (r *githubCommitStatusReporter) rememberGenerationLocked(target githubCommitStatusTarget, runID string, generation uint64) bool {
	if _, ok := r.generations[target]; !ok {
		limit := r.historyLimit
		if limit < 1 {
			limit = 1
		}
		for len(r.generations) >= limit {
			var oldestTarget githubCommitStatusTarget
			oldestTouched := ^uint64(0)
			found := false
			for candidate, state := range r.generations {
				if state.touched < oldestTouched && !r.targetActiveLocked(candidate) {
					oldestTarget = candidate
					oldestTouched = state.touched
					found = true
				}
			}
			if !found {
				return false
			}
			delete(r.generations, oldestTarget)
		}
	}
	r.touched++
	r.generations[target] = githubCommitStatusGeneration{
		runID: runID, generation: generation, touched: r.touched,
	}
	return true
}

func (r *githubCommitStatusReporter) touchGenerationLocked(target githubCommitStatusTarget) {
	state, ok := r.generations[target]
	if !ok {
		return
	}
	r.touched++
	state.touched = r.touched
	r.generations[target] = state
}

func (r *githubCommitStatusReporter) targetActiveLocked(target githubCommitStatusTarget) bool {
	for _, slot := range r.slots {
		if slot.target == target {
			return true
		}
	}
	return false
}

func githubCommitStatusTargetFor(status githubCommitStatus) githubCommitStatusTarget {
	return githubCommitStatusTarget{
		owner:   strings.ToLower(status.Owner),
		repo:    strings.ToLower(status.Repo),
		sha:     strings.ToLower(status.SHA),
		context: "sparkwing/" + status.Pipeline,
	}
}

func (r *githubCommitStatusReporter) run() {
	defer close(r.done)
	for {
		if r.ctx.Err() != nil {
			return
		}
		job, exit := r.takeReady()
		if job != nil {
			r.deliver(*job)
			r.finish(job.key)
			continue
		}
		if exit {
			return
		}
		select {
		case <-r.ctx.Done():
			return
		case <-r.wake:
		}
	}
}

func (r *githubCommitStatusReporter) takeReady() (*githubCommitStatusJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, slot := range r.slots {
		if slot.inFlight || slot.unresolved {
			continue
		}
		var job *githubCommitStatusJob
		if slot.pending != nil {
			job = slot.pending
			slot.pending = nil
		} else if slot.terminal != nil {
			job = slot.terminal
			slot.terminal = nil
		}
		if job != nil {
			slot.inFlight = true
			return job, false
		}
	}
	return nil, !r.accepting && len(r.slots) == 0
}

func (r *githubCommitStatusReporter) finish(key githubCommitStatusKey) {
	r.mu.Lock()
	slot := r.slots[key]
	if slot != nil {
		slot.inFlight = false
		if !slot.unresolved && slot.pending == nil && slot.terminal == nil && (!slot.awaitingTerminal || !r.accepting) {
			delete(r.slots, key)
		}
		r.signalLocked()
	}
	r.mu.Unlock()
}

func (r *githubCommitStatusReporter) signalLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *githubCommitStatusReporter) deliver(job githubCommitStatusJob) {
	postCtx, cancel := context.WithTimeout(r.ctx, githubStatusTimeout)
	defer cancel()
	if err := r.post(postCtx, job.status); err != nil && job.logger != nil {
		job.logger.Warn(
			"github commit status update failed",
			"run_id", job.status.RunID,
			"pipeline", job.status.Pipeline,
			"state", job.status.State,
			"err", err,
		)
	}
}

func logGitHubCommitStatusDrop(logger *slog.Logger, status githubCommitStatus, reason string) {
	if logger == nil {
		return
	}
	logger.Warn(
		"github commit status update dropped",
		"run_id", status.RunID,
		"pipeline", status.Pipeline,
		"state", status.State,
		"reason", reason,
	)
}

func (r *githubCommitStatusReporter) shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.accepting = false
	for key, slot := range r.slots {
		if !slot.inFlight && !slot.unresolved && slot.pending == nil && slot.terminal == nil {
			delete(r.slots, key)
		}
	}
	r.signalLocked()
	r.mu.Unlock()

	select {
	case <-r.done:
		r.cancel()
		return nil
	case <-ctx.Done():
		r.cancel()
		select {
		case <-r.done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func (r *githubCommitStatusReporter) stop() {
	r.mu.Lock()
	r.accepting = false
	r.mu.Unlock()
	r.cancel()
}

func githubCommitStatusFromTrigger(trigger *store.Trigger, runStatus string) (githubCommitStatus, bool) {
	if trigger == nil ||
		trigger.TriggerSource != "github" ||
		trigger.TriggerEnv[sparkwing.EnvGitHubEventName] != sparkwing.EventPullRequest ||
		trigger.GithubOwner == "" ||
		trigger.GithubRepo == "" ||
		trigger.TriggerEnv[sparkwing.EnvPRHeadSHA] == "" ||
		trigger.Pipeline == "" {
		return githubCommitStatus{}, false
	}

	state, description := githubCommitState(runStatus)
	return githubCommitStatus{
		Owner:       trigger.GithubOwner,
		Repo:        trigger.GithubRepo,
		SHA:         trigger.TriggerEnv[sparkwing.EnvPRHeadSHA],
		Pipeline:    trigger.Pipeline,
		RunID:       trigger.ID,
		State:       state,
		Description: description,
	}, true
}

func githubCommitState(runStatus string) (state, description string) {
	switch runStatus {
	case "pending":
		return "pending", "Sparkwing pipeline is running"
	case "success":
		return "success", "Sparkwing pipeline passed"
	case "failed":
		return "failure", "Sparkwing pipeline failed"
	default:
		return "error", "Sparkwing pipeline could not complete"
	}
}

func (r *githubCommitStatusReporter) post(ctx context.Context, status githubCommitStatus) error {
	body, err := json.Marshal(githubCommitStatusRequest{
		State:       status.State,
		TargetURL:   githubRunTargetURL(r.dashboardURL, status.RunID),
		Description: status.Description,
		Context:     "sparkwing/" + status.Pipeline,
	})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/statuses/%s",
		r.apiBaseURL,
		url.PathEscape(status.Owner),
		url.PathEscape(status.Repo),
		url.PathEscape(status.SHA),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sparkwing-controller")
	req.Header.Set("X-GitHub-Api-Version", githubStatusAPIVersion)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("post: github returned %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

func githubRunTargetURL(baseURL, runID string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") ||
		u.Hostname() == "" ||
		u.User != nil ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" ||
		strings.Contains(baseURL, "#") {
		return ""
	}
	u.Scheme = scheme
	u.Path = strings.TrimRight(u.Path, "/") + "/runs"
	u.RawPath = ""
	q := url.Values{}
	q.Set("run", runID)
	u.RawQuery = q.Encode()
	return u.String()
}
