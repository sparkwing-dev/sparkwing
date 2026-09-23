package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type githubAppRepoRef struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type githubAppDelivery struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository githubAppRepoRef `json:"repository"`
}

type githubAppRun struct {
	Pipeline string `json:"pipeline"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
}

type githubAppWebhookResp struct {
	Status string         `json:"status"`
	Reason string         `json:"reason,omitempty"`
	Runs   []githubAppRun `json:"runs,omitempty"`
}

// safety: the replay key covers the team, the pipeline and the signed body, so
// a re-sent delivery is refused however its unsigned delivery header reads.
func githubAppReplayKey(team store.Team, pipeline string, body []byte) string {
	sum := sha256.New()
	sum.Write([]byte("github-app"))
	for _, part := range [][]byte{[]byte(team), []byte(pipeline), body} {
		sum.Write([]byte{0})
		sum.Write(part)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func githubAppIgnored(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusAccepted, githubAppWebhookResp{Status: "ignored", Reason: reason})
}

// safety: the signature is a delivery's only credential, and the installation it names
// picks the team, so a delivery for an installation no team holds does nothing.
func (s *Server) handleGitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookBodyLimit))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err)
		return
	}
	if !s.githubApp.client.VerifyWebhook(r.Header.Get("X-Hub-Signature-256"), body) {
		writeError(w, http.StatusUnauthorized, errors.New("signature mismatch"))
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		writeError(w, http.StatusBadRequest, errors.New("X-GitHub-Delivery header required"))
		return
	}
	var env githubAppDelivery
	if err := json.Unmarshal(body, &env); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode delivery: %w", err))
		return
	}
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
	case "installation":
		s.handleGitHubAppInstallationEvent(w, r, env, delivery)
	case "installation_repositories":
		githubAppIgnored(w, "the repositories an installation covers are read from GitHub when a token is minted")
	case "push", "pull_request":
		s.handleGitHubAppRunEvent(w, r, event, delivery, env, body)
	default:
		githubAppIgnored(w, "event "+event+" starts nothing")
	}
}

// safety: an installation event only removes or pauses a binding; creating one
// needs the connect flow, because GitHub's notice that the App was installed
// says nothing about which team should hold it.
func (s *Server) handleGitHubAppInstallationEvent(w http.ResponseWriter, r *http.Request, env githubAppDelivery, delivery string) {
	id := env.Installation.ID
	op := s.store.AsOperator()
	var err error
	switch env.Action {
	case "deleted":
		var team store.Team
		team, err = op.UnbindGitHubAppInstallation(r.Context(), id)
		if err == nil {
			s.logger.Info("github app installation uninstalled", "team", string(team), "installation_id", id, "delivery", delivery)
		}
	case "suspend", "unsuspend":
		err = op.SetGitHubAppInstallationSuspended(r.Context(), id, env.Action == "suspend", time.Now())
	default:
		githubAppIgnored(w, "installation "+env.Action+" changes no binding")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		githubAppIgnored(w, "no team holds this installation")
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "github app installation event", err)
		return
	}
	writeJSON(w, http.StatusOK, githubAppWebhookResp{Status: env.Action})
}

type githubAppPushPayload struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
	Pusher  struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

type githubAppPullRequestPayload struct {
	Number      int `json:"number"`
	PullRequest struct {
		Head struct {
			Ref  string            `json:"ref"`
			SHA  string            `json:"sha"`
			Repo *githubAppRepoRef `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref  string            `json:"ref"`
			SHA  string            `json:"sha"`
			Repo *githubAppRepoRef `json:"repo"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
}

type githubAppIntake struct {
	user   string
	branch string
	sha    string
	env    map[string]string
	fork   bool
	prInfo *sparkwing.PullRequest
}

func githubAppIntakeFor(event string, env githubAppDelivery, body []byte) (githubAppIntake, string, error) {
	base := map[string]string{
		"GITHUB_REPOSITORY":      env.Repository.FullName,
		envGitHubAppInstallation: strconv.FormatInt(env.Installation.ID, 10),
	}
	if event == "push" {
		var p githubAppPushPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return githubAppIntake{}, "", err
		}
		if p.Deleted {
			return githubAppIntake{}, "branch deleted", nil
		}
		branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
		if !ok || !githubCommit(p.After) {
			return githubAppIntake{}, "not a branch push", nil
		}
		base["GITHUB_BEFORE"], base["GITHUB_AFTER"] = p.Before, p.After
		return githubAppIntake{user: p.Pusher.Name, branch: branch, sha: p.After, env: base}, "", nil
	}
	if _, built := defaultPullRequestActions[env.Action]; !built {
		return githubAppIntake{}, "pull_request action " + env.Action + " starts nothing", nil
	}
	var p githubAppPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return githubAppIntake{}, "", err
	}
	pr := p.PullRequest
	if !githubCommit(pr.Head.SHA) {
		return githubAppIntake{}, "the pull request names no head commit", nil
	}
	base[sparkwing.EnvGitHubEventName] = sparkwing.EventPullRequest
	base[sparkwing.EnvPRNumber] = strconv.Itoa(p.Number)
	base[sparkwing.EnvPRAction] = env.Action
	base[sparkwing.EnvPRBaseRef], base[sparkwing.EnvPRBaseSHA] = pr.Base.Ref, pr.Base.SHA
	base[sparkwing.EnvPRHeadRef], base[sparkwing.EnvPRHeadSHA] = pr.Head.Ref, pr.Head.SHA
	// safety: a head repository that is gone or is not the base repository is
	// someone else's code, whatever the branch is called.
	fork := pr.Head.Repo == nil || pr.Base.Repo == nil || pr.Head.Repo.ID != pr.Base.Repo.ID
	return githubAppIntake{
		user: pr.User.Login, branch: pr.Head.Ref, sha: pr.Head.SHA, env: base, fork: fork,
		prInfo: sparkwing.PullRequestFromEnv(base),
	}, "", nil
}

func (s *Server) handleGitHubAppRunEvent(w http.ResponseWriter, r *http.Request, event, delivery string, env githubAppDelivery, body []byte) {
	repo, ok := store.ParseGitHubRepo(env.Repository.FullName)
	if !ok || env.Repository.ID <= 0 || env.Installation.ID <= 0 {
		githubAppIgnored(w, "the delivery names no repository and installation")
		return
	}
	in, err := s.store.AsOperator().GitHubAppInstallationTeam(r.Context(), env.Installation.ID)
	if errors.Is(err, store.ErrNotFound) {
		githubAppIgnored(w, "no team holds this installation")
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "github app installation", err)
		return
	}
	if in.Suspended {
		githubAppIgnored(w, "the installation is suspended")
		return
	}
	tenant, err := s.tenantForTeam(r.Context(), in.Team)
	if err != nil {
		s.writeInternalError(w, r, "github app team", err)
		return
	}
	subs, err := tenant.GitHubAppTriggersFor(r.Context(), env.Installation.ID, env.Repository.ID)
	if err != nil {
		s.writeInternalError(w, r, "github app triggers", err)
		return
	}
	intake, skip, err := githubAppIntakeFor(event, env, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode %s payload: %w", event, err))
		return
	}
	if skip != "" {
		githubAppIgnored(w, skip)
		return
	}
	var wanted []store.GitHubAppTrigger
	for _, sub := range subs {
		switch {
		case event == "push" && !sub.Push, event == "pull_request" && !sub.PullRequest:
		case intake.fork && !sub.ForkPullRequests:
		default:
			wanted = append(wanted, sub)
		}
	}
	if len(wanted) == 0 {
		reason := "no pipeline of this team subscribes to " + event + " on " + repo.Slug()
		if intake.fork {
			reason = "no pipeline of this team runs pull requests from forks of " + repo.Slug()
		}
		githubAppIgnored(w, reason)
		return
	}
	if !s.admitTriggerSubmission(w, r, githubFloodKey(in.Team, "github-app", repo.Slug()), "github app "+event) {
		return
	}
	resp := githubAppWebhookResp{Status: "dispatched"}
	for _, sub := range wanted {
		run, err := s.startGitHubAppRun(r, tenant, sub, intake, repo, delivery, body)
		if err != nil {
			s.writeInternalError(w, r, "github app run", err)
			return
		}
		resp.Runs = append(resp.Runs, run)
	}
	s.logger.Info("github app delivery accepted", "team", string(in.Team), "event", event,
		"repo", repo.Slug(), "sha", intake.sha, "fork", intake.fork, "runs", len(resp.Runs), "delivery", delivery)
	writeJSON(w, http.StatusAccepted, resp)
}

func (s *Server) startGitHubAppRun(
	r *http.Request, tenant *store.Tenant, sub store.GitHubAppTrigger, in githubAppIntake,
	repo store.GitHubRepo, delivery string, body []byte,
) (githubAppRun, error) {
	ctx := r.Context()
	replayKey := githubAppReplayKey(tenant.Team(), sub.Pipeline, body)
	if existing, err := tenant.FindTriggerByWebhookReplay(ctx, replayKey, delivery+"/"+sub.Pipeline); err == nil && existing != nil {
		return githubAppRun{Pipeline: sub.Pipeline, RunID: existing.ID, Status: "duplicate"}, nil
	}
	runID := newRunID()
	triggerEnv := map[string]string{"GITHUB_DELIVERY": delivery}
	for k, v := range in.env {
		triggerEnv[k] = v
	}
	trigger := sparkwing.TriggerInfo{Source: "github", User: in.user, PullRequest: in.prInfo}
	err := tenant.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: sub.Pipeline, TriggerSource: trigger.Source, TriggerUser: trigger.User,
		TriggerEnv: triggerEnv, GitBranch: in.branch, GitSHA: in.sha, Repo: repo.Slug(),
		GithubOwner: repo.Owner, GithubRepo: repo.Name,
		WebhookDelivery: delivery + "/" + sub.Pipeline, WebhookReplayKey: replayKey,
		Untrusted: in.fork, CreatedAt: time.Now(),
	})
	if errors.Is(err, store.ErrDuplicateWebhookDelivery) {
		existing, ferr := tenant.FindTriggerByWebhookReplay(ctx, replayKey, delivery+"/"+sub.Pipeline)
		if ferr != nil || existing == nil {
			return githubAppRun{Pipeline: sub.Pipeline, Status: "duplicate"}, nil
		}
		return githubAppRun{Pipeline: sub.Pipeline, RunID: existing.ID, Status: "duplicate"}, nil
	}
	if err != nil {
		return githubAppRun{}, fmt.Errorf("persist trigger: %w", err)
	}
	pendingStatus := s.reserveGitHubCommitStatus(ctx, runID, "pending")
	dispatched := false
	defer func() { pendingStatus(dispatched) }()
	s.recordQueueActivity(time.Now())
	if err := s.dispatcher.Dispatch(ctx, RunRequest{
		RunID: runID, Pipeline: sub.Pipeline, Trigger: trigger,
		Git: &sparkwing.Git{Branch: in.branch, SHA: in.sha, Repo: repo.Slug()},
	}); err != nil {
		return githubAppRun{}, err
	}
	dispatched = true
	return githubAppRun{Pipeline: sub.Pipeline, RunID: runID, Status: "dispatched"}, nil
}
