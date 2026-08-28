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
	githubAPIBaseURL       = "https://api.github.com"
	githubStatusAPIVersion = "2022-11-28"
	githubStatusTimeout    = 10 * time.Second
	githubStatusQueueSize  = 64
)

type githubCommitStatusReporter struct {
	token        string
	dashboardURL string
	apiBaseURL   string
	httpClient   *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	jobs         chan githubCommitStatusJob
	done         chan struct{}
	mu           sync.Mutex
	accepting    bool
}

type githubCommitStatusJob struct {
	logger           *slog.Logger
	status           githubCommitStatus
	dispatchAccepted <-chan bool
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
// HTTP(S), include a host, and omit credentials, query, and fragment.
func (s *Server) WithGitHubCommitStatuses(token, dashboardURL string) *Server {
	token = strings.TrimSpace(token)
	if token == "" {
		s.githubCommitStatuses = nil
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
		jobs:         make(chan githubCommitStatusJob, capacity),
		done:         make(chan struct{}),
		accepting:    true,
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
	return r.enqueueAfter(logger, status, nil)
}

func (r *githubCommitStatusReporter) reserve(logger *slog.Logger, status githubCommitStatus) func(bool) {
	dispatchAccepted := make(chan bool, 1)
	r.enqueueAfter(logger, status, dispatchAccepted)
	var once sync.Once
	return func(accepted bool) {
		once.Do(func() {
			dispatchAccepted <- accepted
			close(dispatchAccepted)
		})
	}
}

func (r *githubCommitStatusReporter) enqueueAfter(logger *slog.Logger, status githubCommitStatus, dispatchAccepted <-chan bool) bool {
	job := githubCommitStatusJob{
		logger:           logger,
		status:           status,
		dispatchAccepted: dispatchAccepted,
	}
	r.mu.Lock()
	accepted := false
	if r.accepting {
		select {
		case r.jobs <- job:
			accepted = true
		default:
		}
	}
	r.mu.Unlock()
	if !accepted && logger != nil {
		logger.Warn(
			"github commit status update dropped",
			"run_id", status.RunID,
			"pipeline", status.Pipeline,
			"state", status.State,
		)
	}
	return accepted
}

func (r *githubCommitStatusReporter) run() {
	defer close(r.done)
	for {
		select {
		case <-r.ctx.Done():
			return
		case job, ok := <-r.jobs:
			if !ok {
				return
			}
			r.deliver(job)
		}
	}
}

func (r *githubCommitStatusReporter) deliver(job githubCommitStatusJob) {
	if job.dispatchAccepted != nil {
		select {
		case accepted := <-job.dispatchAccepted:
			if !accepted {
				return
			}
		case <-r.ctx.Done():
			return
		}
	}
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

func (r *githubCommitStatusReporter) shutdown(ctx context.Context) error {
	r.mu.Lock()
	if r.accepting {
		r.accepting = false
		close(r.jobs)
	}
	r.mu.Unlock()

	select {
	case <-r.done:
		r.cancel()
		return nil
	case <-ctx.Done():
		r.cancel()
		return ctx.Err()
	}
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
	if err != nil ||
		(u.Scheme != "http" && u.Scheme != "https") ||
		u.Host == "" ||
		u.User != nil ||
		u.RawQuery != "" ||
		u.ForceQuery ||
		u.Fragment != "" ||
		strings.Contains(baseURL, "#") {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/runs"
	u.RawPath = ""
	q := url.Values{}
	q.Set("run", runID)
	u.RawQuery = q.Encode()
	return u.String()
}
