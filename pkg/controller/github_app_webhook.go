package controller

import (
	"context"
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
		// safety: the covering answers kept for a minute are dropped, so a
		// repository removed on GitHub starts nothing from the next delivery on.
		s.githubApp.forgetCovering()
		githubAppIgnored(w, "the repositories an installation covers are read from GitHub when a run starts or a token is minted")
	case "push", "pull_request", "release", "create", "delete":
		s.handleGitHubAppRunEvent(w, r, event, delivery, env, body)
	case "repository":
		s.handleGitHubAppRepositoryEvent(w, r, env)
	case "check_run", "check_suite":
		s.handleGitHubAppCheckEvent(w, r, event, delivery, env, body)
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
	Repository struct {
		PushedAt json.RawMessage `json:"pushed_at"`
	} `json:"repository"`
}

type githubAppPullRequestPayload struct {
	Number int `json:"number"`
	Label  struct {
		Name string `json:"name"`
	} `json:"label"`
	PullRequest struct {
		UpdatedAt string `json:"updated_at"`
		Merged    bool   `json:"merged"`
		Head      struct {
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
	// at is when GitHub says the event happened, zero when the payload
	// does not say.
	at     time.Time
	prInfo *sparkwing.PullRequest
}

// githubPushedAt reads a push payload's repository.pushed_at, which GitHub
// writes as Unix seconds in push events and as a timestamp elsewhere.
func githubPushedAt(raw json.RawMessage) time.Time {
	var secs int64
	if json.Unmarshal(raw, &secs) == nil && secs > 0 {
		return time.Unix(secs, 0)
	}
	var stamp string
	if json.Unmarshal(raw, &stamp) == nil {
		if at, err := time.Parse(time.RFC3339, stamp); err == nil {
			return at
		}
	}
	return time.Time{}
}

func githubAppIntakeFor(event string, env githubAppDelivery, body []byte) (githubAppIntake, string, error) {
	base := map[string]string{
		"GITHUB_REPOSITORY":      env.Repository.FullName,
		"GITHUB_EVENT_NAME":      event,
		"GITHUB_ACTION":          env.Action,
		envGitHubAppInstallation: strconv.FormatInt(env.Installation.ID, 10),
	}
	if event == "push" {
		var p githubAppPushPayload
		if err := json.Unmarshal(body, &p); err != nil {
			return githubAppIntake{}, "", err
		}
		if p.Deleted || strings.Trim(p.After, "0") == "" {
			return githubAppIntake{}, "branch deleted", nil
		}
		branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
		if !ok || !githubCommit(p.After) {
			return githubAppIntake{}, "not a branch push", nil
		}
		base["GITHUB_BEFORE"], base["GITHUB_AFTER"] = p.Before, p.After
		base["GITHUB_REF"], base["GITHUB_REF_TYPE"] = p.Ref, "branch"
		return githubAppIntake{
			user: p.Pusher.Name, branch: branch, sha: p.After, env: base, at: githubPushedAt(p.Repository.PushedAt),
		}, "", nil
	}
	if event == "release" {
		var p struct {
			Release struct {
				TagName     string `json:"tag_name"`
				PublishedAt string `json:"published_at"`
				Author      struct {
					Login string `json:"login"`
				} `json:"author"`
			} `json:"release"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return githubAppIntake{}, "", err
		}
		if env.Action != "published" && env.Action != "prereleased" {
			return githubAppIntake{}, "release action starts nothing", nil
		}
		if p.Release.TagName == "" || strings.Contains(p.Release.TagName, "..") || strings.HasPrefix(p.Release.TagName, "/") {
			return githubAppIntake{}, "invalid release tag", nil
		}
		base["GITHUB_REF"], base["GITHUB_REF_TYPE"], base["GITHUB_TAG_NAME"] = "refs/tags/"+p.Release.TagName, "tag", p.Release.TagName
		at, _ := time.Parse(time.RFC3339, p.Release.PublishedAt)
		return githubAppIntake{user: p.Release.Author.Login, branch: p.Release.TagName, env: base, at: at}, "", nil
	}
	if event == "create" || event == "delete" {
		var p struct {
			Ref          string `json:"ref"`
			RefType      string `json:"ref_type"`
			MasterBranch string `json:"master_branch"`
			Repository   struct {
				PushedAt  json.RawMessage `json:"pushed_at"`
				UpdatedAt json.RawMessage `json:"updated_at"`
			} `json:"repository"`
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return githubAppIntake{}, "", err
		}
		if p.RefType != "branch" || p.Ref == "" || strings.Contains(p.Ref, "..") || strings.HasPrefix(p.Ref, "/") {
			return githubAppIntake{}, "not a branch event", nil
		}
		if event == "delete" && p.MasterBranch == "" {
			return githubAppIntake{}, "branch deletion names no default branch", nil
		}
		base["GITHUB_REF"], base["GITHUB_REF_TYPE"] = "refs/heads/"+p.Ref, "branch"
		at := githubPushedAt(p.Repository.PushedAt)
		if updated := githubPushedAt(p.Repository.UpdatedAt); updated.After(at) {
			at = updated
		}
		branch := p.Ref
		if event == "delete" {
			branch = p.MasterBranch
		}
		return githubAppIntake{user: p.Sender.Login, branch: branch, env: base, at: at}, "", nil
	}
	if event != "pull_request" {
		return githubAppIntake{}, "unknown event", nil
	}
	if _, built := defaultPullRequestActions[env.Action]; !built && env.Action != "closed" && env.Action != "labeled" && env.Action != "ready_for_review" {
		return githubAppIntake{}, "pull_request action " + env.Action + " starts nothing", nil
	}
	var p githubAppPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return githubAppIntake{}, "", err
	}
	pr := p.PullRequest
	if p.Number <= 0 {
		return githubAppIntake{}, "the pull request has no number", nil
	}
	// safety: a head repository that is gone or is not the base repository is
	// someone else's code, whatever the branch is called, and nothing runs it.
	if pr.Head.Repo == nil || pr.Base.Repo == nil || pr.Head.Repo.ID != pr.Base.Repo.ID {
		return githubAppIntake{}, githubAppForkReason, nil
	}
	if !githubCommit(pr.Head.SHA) {
		return githubAppIntake{}, "the pull request names no head commit", nil
	}
	base[sparkwing.EnvGitHubEventName] = sparkwing.EventPullRequest
	base[sparkwing.EnvPRNumber] = strconv.Itoa(p.Number)
	base[sparkwing.EnvPRAction] = env.Action
	base[sparkwing.EnvPRBaseRef], base[sparkwing.EnvPRBaseSHA] = pr.Base.Ref, pr.Base.SHA
	base[sparkwing.EnvPRHeadRef], base[sparkwing.EnvPRHeadSHA] = pr.Head.Ref, pr.Head.SHA
	base["GITHUB_REF"], base["GITHUB_REF_TYPE"] = "refs/pull/"+strconv.Itoa(p.Number)+"/head", "branch"
	base["GITHUB_LABEL"] = p.Label.Name
	base["GITHUB_MERGED"] = strconv.FormatBool(pr.Merged)
	// safety: an unreadable updated_at leaves the time zero, and the caller
	// refuses an undated event.
	updated, err := time.Parse(time.RFC3339, pr.UpdatedAt)
	if err != nil {
		updated = time.Time{}
	}
	return githubAppIntake{
		user: pr.User.Login, branch: pr.Head.Ref, sha: pr.Head.SHA, env: base, at: updated,
		prInfo: sparkwing.PullRequestFromEnv(base),
	}, "", nil
}

const githubAppForkReason = "pull requests from forks are not run"

// githubAppClockSkew is how far GitHub's clock may run behind this
// controller's before a delivery reads as older than its binding.
const githubAppClockSkew = time.Minute

func githubAppDeliveryDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// githubAppBinding resolves the team holding the installation a delivery
// names. When no team holds it, it answers the delivery and returns false.
func (s *Server) githubAppBinding(w http.ResponseWriter, r *http.Request, env githubAppDelivery) (store.GitHubAppInstallation, *store.Tenant, store.GitHubRepo, bool) {
	ctx := r.Context()
	repo, ok := store.ParseGitHubRepo(env.Repository.FullName)
	if !ok || env.Repository.ID <= 0 || env.Installation.ID <= 0 {
		githubAppIgnored(w, "the delivery names no repository and installation")
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	repo.ID = env.Repository.ID
	in, err := s.store.AsOperator().GitHubAppInstallationTeam(ctx, env.Installation.ID)
	if errors.Is(err, store.ErrNotFound) {
		githubAppIgnored(w, "no team holds this installation")
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "github app installation", err)
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	if in.Suspended {
		githubAppIgnored(w, "the installation is suspended")
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	tenant, err := s.tenantForTeam(ctx, in.Team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		githubAppIgnored(w, "no team holds this installation")
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	if err != nil {
		s.writeInternalError(w, r, "github app team", err)
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	return in, tenant, repo, true
}

func (s *Server) handleGitHubAppRunEvent(w http.ResponseWriter, r *http.Request, event, delivery string, env githubAppDelivery, body []byte) {
	ctx := r.Context()
	in, tenant, repo, ok := s.githubAppBinding(w, r, env)
	if !ok {
		return
	}
	intake, skip, err := githubAppIntakeFor(event, env, body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode %s payload: %w", event, err))
		return
	}
	if skip == githubAppForkReason {
		s.logger.Info("github app fork pull request ignored", "team", string(in.Team), "repo", repo.Slug(),
			"installation_id", env.Installation.ID, "delivery", delivery)
	}
	if skip != "" {
		githubAppIgnored(w, skip)
		return
	}
	// safety: an event from before this team held the installation was meant
	// for whoever held it then, so a replay of it after a move starts nothing,
	// and an event whose time cannot be read cannot show it is not one.
	if intake.at.IsZero() {
		s.logger.Info("github app undated event ignored", "team", string(in.Team), "event", event,
			"repo", repo.Slug(), "delivery", delivery)
		githubAppIgnored(w, "the event carries no time this controller can read")
		return
	}
	if intake.at.Add(githubAppClockSkew).Before(in.CreatedAt) {
		githubAppIgnored(w, "the event predates this team's connection of the installation")
		return
	}
	subs, err := tenant.GitHubAppTriggersFor(ctx, env.Installation.ID, env.Repository.ID)
	if err != nil {
		s.writeInternalError(w, r, "github app triggers", err)
		return
	}
	var planned []githubAppPlannedRun
	for _, sub := range subs {
		if githubAppSubscribes(sub, event, env.Action, intake.env["GITHUB_LABEL"]) {
			planned = append(planned, githubAppPlannedRun{pipeline: sub.Pipeline, intake: intake})
		}
	}
	if len(planned) == 0 {
		githubAppIgnored(w, "no pipeline of this team subscribes to "+event+" on "+repo.Slug())
		return
	}
	s.startGitHubAppRuns(w, r, in, tenant, env, repo, event, delivery, body, planned)
}

func (s *Server) handleGitHubAppRepositoryEvent(w http.ResponseWriter, r *http.Request, env githubAppDelivery) {
	if env.Action != "renamed" && env.Action != "transferred" {
		githubAppIgnored(w, "repository action starts nothing")
		return
	}
	_, tenant, repo, ok := s.githubAppBinding(w, r, env)
	if !ok {
		return
	}
	inst, covered, err := s.githubApp.liveCoveringInstallation(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !covered || inst.ID != env.Installation.ID || inst.SuspendedAt != nil {
		githubAppIgnored(w, "the installation no longer covers "+repo.Slug())
		return
	}
	repositories, err := s.githubApp.client.InstallationRepositories(r.Context(), env.Installation.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	matched := false
	for _, current := range repositories {
		if current.ID == repo.ID && strings.EqualFold(current.FullName, repo.Slug()) {
			matched = true
			break
		}
	}
	if !matched {
		githubAppIgnored(w, "the installation no longer covers repository id")
		return
	}
	if err := tenant.RenameGitHubAppTriggerRepository(r.Context(), env.Installation.ID, repo.ID, repo.Slug()); err != nil {
		s.writeInternalError(w, r, "update github app repository", err)
		return
	}
	writeJSON(w, http.StatusOK, githubAppWebhookResp{Status: "updated"})
}

func githubAppSubscribes(sub store.GitHubAppTrigger, event, action, label string) bool {
	switch event {
	case "push":
		return sub.Push
	case "pull_request":
		switch action {
		case "opened", "synchronize", "reopened":
			return sub.PullRequest
		case "closed":
			return sub.PullRequestClosed
		case "ready_for_review":
			return sub.PullRequestReadyForReview
		case "labeled":
			if !sub.PullRequestLabeled {
				return false
			}
			for _, want := range sub.PullRequestLabels {
				if strings.EqualFold(want, label) {
					return true
				}
			}
		}
	case "release":
		return action == "published" && sub.ReleasePublished || action == "prereleased" && sub.ReleasePrereleased
	case "create":
		return sub.BranchCreate
	case "delete":
		return sub.BranchDelete
	}
	return false
}

// githubAppPlannedRun is one run a delivery would start.
type githubAppPlannedRun struct {
	pipeline string
	intake   githubAppIntake
}

// startGitHubAppRuns starts planned, the runs a delivery bound to in asks
// for, once per signed body and within the team's run budget.
func (s *Server) startGitHubAppRuns(w http.ResponseWriter, r *http.Request, in store.GitHubAppInstallation,
	tenant *store.Tenant, env githubAppDelivery, repo store.GitHubRepo, event, delivery string, body []byte,
	planned []githubAppPlannedRun,
) {
	ctx := r.Context()
	// safety: the digest is of the signed body and is kept for every team, so
	// a redelivery spends no run budget and a replay after the installation
	// moves teams starts nothing in the new one.
	digest := githubAppDeliveryDigest(body)
	seen, err := s.store.GitHubAppDeliverySeen(ctx, digest)
	if err != nil {
		s.writeInternalError(w, r, "github app delivery", err)
		return
	}
	if seen {
		s.writeGitHubAppDuplicate(w, r, tenant, planned, delivery, body)
		return
	}
	// safety: GitHub is asked on every delivery rather than through the cache
	// the token route keeps, because a cached answer lags a removal that no
	// installation_repositories delivery has reached this replica for yet.
	gh, covered, err := s.githubApp.liveCoveringInstallation(ctx, repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not be reached to find the repository's installation"))
		return
	}
	if !covered || gh.ID != env.Installation.ID || gh.SuspendedAt != nil {
		githubAppIgnored(w, "the installation no longer covers "+repo.Slug())
		return
	}
	if event == "release" || event == "create" || event == "delete" {
		sha := ""
		for _, plan := range planned {
			existing, err := tenant.FindTriggerByWebhookReplay(ctx,
				githubAppReplayKey(tenant.Team(), plan.pipeline, body), delivery+"/"+plan.pipeline)
			if err == nil && existing != nil && existing.GitSHA != "" {
				sha = existing.GitSHA
				break
			}
		}
		if sha == "" {
			ref := planned[0].intake.env["GITHUB_REF"]
			if event == "delete" {
				ref = "refs/heads/" + planned[0].intake.branch
			}
			var err error
			sha, err = s.githubApp.client.ResolveCommit(ctx, env.Installation.ID, repo.Owner, repo.Name, ref)
			if err != nil {
				writeError(w, http.StatusBadGateway, fmt.Errorf("resolve GitHub ref: %w", err))
				return
			}
		}
		for i := range planned {
			planned[i].intake.sha = sha
		}
	}
	resp := githubAppWebhookResp{Status: "dispatched"}
	shed := 0
	for _, plan := range planned {
		// safety: a run this delivery already started answers without spending,
		// so a redelivery after a partial shed pays only for what was shed.
		if existing, err := tenant.FindTriggerByWebhookReplay(ctx,
			githubAppReplayKey(tenant.Team(), plan.pipeline, body), delivery+"/"+plan.pipeline); err == nil && existing != nil {
			resp.Runs = append(resp.Runs, githubAppRun{Pipeline: plan.pipeline, RunID: existing.ID, Status: "duplicate"})
			continue
		}
		// safety: each run spends one of the team's budget, so a repository
		// with many subscriptions, or a team with many repositories, cannot
		// multiply what one delivery or one hour creates.
		if refusal := s.triggerFloodRefusal(ctx, githubAppFloodKey(in.Team), "github app "+event); refusal != nil {
			if !githubAppStartedAny(resp.Runs) {
				refusal.write(w)
				return
			}
			shed++
			resp.Runs = append(resp.Runs, githubAppRun{Pipeline: plan.pipeline, Status: "shed"})
			continue
		}
		run, err := s.startGitHubAppRun(r, tenant, plan, repo, delivery, body)
		if err != nil {
			writeIdentityError(w, s, r, "github app run", err)
			return
		}
		resp.Runs = append(resp.Runs, run)
	}
	// safety: a delivery that shed a run is not recorded, so GitHub's redelivery
	// can still start it; the runs it did start answer as duplicates.
	if shed == 0 {
		if err := s.store.RecordGitHubAppDelivery(ctx, digest, delivery, time.Now()); err != nil {
			s.logger.Warn("github app delivery not recorded", "delivery", delivery, "err", err)
		}
	}
	s.logger.Info("github app delivery accepted", "team", string(in.Team), "event", event,
		"repo", repo.Slug(), "sha", planned[0].intake.sha, "runs", len(resp.Runs), "shed", shed, "delivery", delivery)
	writeJSON(w, http.StatusAccepted, resp)
}

func githubAppStartedAny(runs []githubAppRun) bool {
	for _, run := range runs {
		if run.RunID != "" {
			return true
		}
	}
	return false
}

// githubAppFloodKey is the budget App deliveries spend: the team's own, the
// one its API submissions spend.
func githubAppFloodKey(team store.Team) string {
	return "team:" + string(team)
}

func (s *Server) writeGitHubAppDuplicate(w http.ResponseWriter, r *http.Request, tenant *store.Tenant,
	planned []githubAppPlannedRun, delivery string, body []byte,
) {
	resp := githubAppWebhookResp{Status: "duplicate"}
	for _, plan := range planned {
		run := githubAppRun{Pipeline: plan.pipeline, Status: "duplicate"}
		existing, err := tenant.FindTriggerByWebhookReplay(r.Context(),
			githubAppReplayKey(tenant.Team(), plan.pipeline, body), delivery+"/"+plan.pipeline)
		if err == nil && existing != nil {
			run.RunID = existing.ID
		}
		resp.Runs = append(resp.Runs, run)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) startGitHubAppRun(
	r *http.Request, tenant *store.Tenant, plan githubAppPlannedRun, repo store.GitHubRepo, delivery string, body []byte,
) (githubAppRun, error) {
	ctx := r.Context()
	pipeline, in := plan.pipeline, plan.intake
	replayKey := githubAppReplayKey(tenant.Team(), pipeline, body)
	if existing, err := tenant.FindTriggerByWebhookReplay(ctx, replayKey, delivery+"/"+pipeline); err == nil && existing != nil {
		return githubAppRun{Pipeline: pipeline, RunID: existing.ID, Status: "duplicate"}, nil
	}
	runID := newRunID()
	triggerEnv := map[string]string{"GITHUB_DELIVERY": delivery}
	for k, v := range in.env {
		triggerEnv[k] = v
	}
	trigger := sparkwing.TriggerInfo{Source: "github", User: in.user, PullRequest: in.prInfo}
	err := tenant.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: pipeline, TriggerSource: trigger.Source, TriggerUser: trigger.User,
		TriggerEnv: triggerEnv, GitBranch: in.branch, GitSHA: in.sha, Repo: repo.Slug(),
		GithubOwner: repo.Owner, GithubRepo: repo.Name, GithubRepoID: repo.ID,
		WebhookDelivery: delivery + "/" + pipeline, WebhookReplayKey: replayKey,
		CreatedAt: time.Now(),
	})
	if errors.Is(err, store.ErrDuplicateWebhookDelivery) {
		run := githubAppRun{Pipeline: pipeline, Status: "duplicate"}
		if existing, ferr := tenant.FindTriggerByWebhookReplay(ctx, replayKey, delivery+"/"+pipeline); ferr == nil && existing != nil {
			run.RunID = existing.ID
		}
		return run, nil
	}
	if err != nil {
		return githubAppRun{}, fmt.Errorf("persist trigger: %w", err)
	}
	s.recordQueueActivity(time.Now())
	if err := s.dispatcher.Dispatch(ctx, RunRequest{
		RunID: runID, Pipeline: pipeline, Trigger: trigger,
		Git: &sparkwing.Git{Branch: in.branch, SHA: in.sha, Repo: repo.Slug()},
	}); err != nil {
		return githubAppRun{}, err
	}
	s.reportGitHubRunState(context.WithoutCancel(ctx), runID, "pending")
	return githubAppRun{Pipeline: pipeline, RunID: runID, Status: "dispatched"}, nil
}
