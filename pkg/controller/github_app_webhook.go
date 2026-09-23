package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
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
	Repository struct {
		PushedAt json.RawMessage `json:"pushed_at"`
	} `json:"repository"`
}

type githubAppPullRequestPayload struct {
	Number      int `json:"number"`
	PullRequest struct {
		UpdatedAt string `json:"updated_at"`
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
	tag    string
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

func githubTagMatches(patterns []string, tag string) bool {
	for _, pattern := range patterns {
		if matched, err := path.Match(pattern, tag); err == nil && matched {
			return true
		}
	}
	return false
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
		if p.Deleted || strings.Trim(p.After, "0") == "" {
			if strings.HasPrefix(p.Ref, "refs/tags/") {
				return githubAppIntake{}, "tag deleted", nil
			}
			return githubAppIntake{}, "branch deleted", nil
		}
		branch, isBranch := strings.CutPrefix(p.Ref, "refs/heads/")
		tag, isTag := strings.CutPrefix(p.Ref, "refs/tags/")
		if (!isBranch && !isTag) || (isBranch && branch == "") || (isTag && tag == "") || !githubCommit(p.After) {
			return githubAppIntake{}, "not a branch or tag push", nil
		}
		if !isBranch {
			branch = ""
		}
		if !isTag {
			tag = ""
		}
		base["GITHUB_BEFORE"], base["GITHUB_AFTER"] = p.Before, p.After
		base["GITHUB_REF"] = p.Ref
		base[sparkwing.EnvGitHubEventName] = githubEventPush
		if isTag {
			base["GITHUB_REF_TYPE"], base["GITHUB_TAG"] = "tag", tag
		} else {
			base["GITHUB_REF_TYPE"] = "branch"
		}
		return githubAppIntake{
			user: p.Pusher.Name, branch: branch, tag: tag, sha: p.After, env: base, at: githubPushedAt(p.Repository.PushedAt),
		}, "", nil
	}
	if _, built := defaultPullRequestActions[env.Action]; !built {
		return githubAppIntake{}, "pull_request action " + env.Action + " starts nothing", nil
	}
	var p githubAppPullRequestPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return githubAppIntake{}, "", err
	}
	pr := p.PullRequest
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

func (s *Server) handleGitHubAppRunEvent(w http.ResponseWriter, r *http.Request, event, delivery string, env githubAppDelivery, body []byte) {
	ctx := r.Context()
	repo, ok := store.ParseGitHubRepo(env.Repository.FullName)
	if !ok || env.Repository.ID <= 0 || env.Installation.ID <= 0 {
		githubAppIgnored(w, "the delivery names no repository and installation")
		return
	}
	in, err := s.store.AsOperator().GitHubAppInstallationTeam(ctx, env.Installation.ID)
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
	tenant, err := s.tenantForTeam(ctx, in.Team)
	if errors.Is(err, store.ErrUnknownTeam) || errors.Is(err, store.ErrNoTeam) {
		githubAppIgnored(w, "no team holds this installation")
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "github app team", err)
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
	var wanted []store.GitHubAppTrigger
	for _, sub := range subs {
		if (event == "push" && ((intake.tag != "" && githubTagMatches(sub.Tags, intake.tag)) || (intake.tag == "" && sub.Push))) ||
			(event == "pull_request" && sub.PullRequest) {
			wanted = append(wanted, sub)
		}
	}
	if len(wanted) == 0 {
		githubAppIgnored(w, "no pipeline of this team subscribes to "+event+" on "+repo.Slug())
		return
	}
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
		s.writeGitHubAppDuplicate(w, r, tenant, wanted, delivery, body)
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
	resp := githubAppWebhookResp{Status: "dispatched"}
	shed := 0
	for _, sub := range wanted {
		// safety: a run this delivery already started answers without spending,
		// so a redelivery after a partial shed pays only for what was shed.
		if existing, err := tenant.FindTriggerByWebhookReplay(ctx,
			githubAppReplayKey(tenant.Team(), sub.Pipeline, body), delivery+"/"+sub.Pipeline); err == nil && existing != nil {
			resp.Runs = append(resp.Runs, githubAppRun{Pipeline: sub.Pipeline, RunID: existing.ID, Status: "duplicate"})
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
			resp.Runs = append(resp.Runs, githubAppRun{Pipeline: sub.Pipeline, Status: "shed"})
			continue
		}
		run, err := s.startGitHubAppRun(r, tenant, sub, intake, repo, delivery, body)
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
		"repo", repo.Slug(), "sha", intake.sha, "runs", len(resp.Runs), "shed", shed, "delivery", delivery)
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
	wanted []store.GitHubAppTrigger, delivery string, body []byte,
) {
	resp := githubAppWebhookResp{Status: "duplicate"}
	for _, sub := range wanted {
		run := githubAppRun{Pipeline: sub.Pipeline, Status: "duplicate"}
		existing, err := tenant.FindTriggerByWebhookReplay(r.Context(),
			githubAppReplayKey(tenant.Team(), sub.Pipeline, body), delivery+"/"+sub.Pipeline)
		if err == nil && existing != nil {
			run.RunID = existing.ID
		}
		resp.Runs = append(resp.Runs, run)
	}
	writeJSON(w, http.StatusOK, resp)
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
		CreatedAt: time.Now(),
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
