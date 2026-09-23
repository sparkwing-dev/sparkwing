package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type githubAppCheckPullRequest struct {
	Head struct {
		Repo *githubAppRepoRef `json:"repo"`
	} `json:"head"`
	Base struct {
		Repo *githubAppRepoRef `json:"repo"`
	} `json:"base"`
}

type githubAppCheckApp struct {
	ID int64 `json:"id"`
}

type githubAppCheckPayload struct {
	CheckRun *struct {
		Name         string                      `json:"name"`
		HeadSHA      string                      `json:"head_sha"`
		ExternalID   string                      `json:"external_id"`
		App          githubAppCheckApp           `json:"app"`
		PullRequests []githubAppCheckPullRequest `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite *struct {
		HeadSHA      string                      `json:"head_sha"`
		App          githubAppCheckApp           `json:"app"`
		PullRequests []githubAppCheckPullRequest `json:"pull_requests"`
	} `json:"check_suite"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// handleGitHubAppCheckEvent re-runs what a person asks GitHub to re-run:
// check_run rerequested runs that check run's pipeline again, and
// check_suite rerequested runs each subscribed pipeline with its own prior
// App run on this repository and commit, copying that run's branch and pull
// request when its event remains subscribed.
func (s *Server) handleGitHubAppCheckEvent(w http.ResponseWriter, r *http.Request, event, delivery string, env githubAppDelivery, body []byte) {
	switch {
	case env.Action == "rerequested":
	case event == "check_suite" && env.Action == "requested":
		// safety: GitHub asks for a suite on every push, and the push delivery
		// already starts its runs, so answering this too would run them twice.
		githubAppIgnored(w, "the push delivery starts a new commit's runs")
		return
	default:
		githubAppIgnored(w, event+" "+env.Action+" starts nothing")
		return
	}
	var p githubAppCheckPayload
	if err := json.Unmarshal(body, &p); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode %s payload: %w", event, err))
		return
	}
	var sha string
	var app githubAppCheckApp
	var prs []githubAppCheckPullRequest
	switch {
	case event == "check_run" && p.CheckRun != nil:
		sha, app, prs = p.CheckRun.HeadSHA, p.CheckRun.App, p.CheckRun.PullRequests
	case event == "check_suite" && p.CheckSuite != nil:
		sha, app, prs = p.CheckSuite.HeadSHA, p.CheckSuite.App, p.CheckSuite.PullRequests
	default:
		githubAppIgnored(w, "the delivery names no "+event)
		return
	}
	if app.ID != s.githubApp.client.AppID() {
		githubAppIgnored(w, "the "+event+" belongs to another App")
		return
	}
	if !githubCommit(sha) {
		githubAppIgnored(w, "the "+event+" names no head commit")
		return
	}
	in, tenant, repo, ok := s.githubAppBinding(w, r, env)
	if !ok {
		return
	}
	for _, pr := range prs {
		if pr.Head.Repo == nil || pr.Base.Repo == nil || pr.Head.Repo.ID != pr.Base.Repo.ID {
			s.logger.Info("github app fork pull request re-run ignored", "team", string(in.Team), "repo", repo.Slug(),
				"installation_id", env.Installation.ID, "delivery", delivery)
			githubAppIgnored(w, githubAppForkReason)
			return
		}
	}
	subs, err := tenant.GitHubAppTriggersFor(r.Context(), env.Installation.ID, env.Repository.ID)
	if err != nil {
		s.writeInternalError(w, r, "github app triggers", err)
		return
	}
	subscribed := map[string]store.GitHubAppTrigger{}
	for _, sub := range subs {
		if sub.Push || sub.PullRequest || len(sub.Tags) > 0 {
			subscribed[sub.Pipeline] = sub
		}
	}
	runs, err := tenant.GitHubCommitTriggers(r.Context(), repo, sha)
	if err != nil {
		s.writeInternalError(w, r, "github app commit runs", err)
		return
	}
	installation := strconv.FormatInt(env.Installation.ID, 10)
	var anchors []*store.Trigger
	for _, run := range runs {
		if run.TriggerEnv[envGitHubAppInstallation] == installation && run.GitSHA == sha {
			anchors = append(anchors, run)
		}
	}
	var planned []githubAppPlannedRun
	if event == "check_run" {
		pipeline, named := strings.CutPrefix(p.CheckRun.Name, "sparkwing/")
		for _, run := range anchors {
			if sub, ok := subscribed[pipeline]; named && ok && run.ID == p.CheckRun.ExternalID && run.Pipeline == pipeline && githubAppRunEventSubscribed(run, sub) {
				planned = append(planned, githubAppPlannedRun{pipeline: pipeline, intake: githubAppRerunIntake(run, p.Sender.Login)})
				break
			}
		}
	} else {
		for _, sub := range subs {
			if !sub.Push && !sub.PullRequest && len(sub.Tags) == 0 {
				continue
			}
			if run := githubAppRerunAnchor(anchors, sub); run != nil {
				planned = append(planned, githubAppPlannedRun{pipeline: sub.Pipeline, intake: githubAppRerunIntake(run, p.Sender.Login)})
			}
		}
	}
	if len(planned) == 0 {
		githubAppIgnored(w, "no run of a subscribed pipeline of this team at "+sha+" to re-run")
		return
	}
	s.startGitHubAppRuns(w, r, in, tenant, env, repo, event, delivery, body, planned)
}

func githubAppRerunAnchor(anchors []*store.Trigger, sub store.GitHubAppTrigger) *store.Trigger {
	for _, run := range anchors {
		if run.Pipeline == sub.Pipeline && githubAppRunEventSubscribed(run, sub) {
			return run
		}
	}
	return nil
}

func githubAppRunEventSubscribed(run *store.Trigger, sub store.GitHubAppTrigger) bool {
	event := run.TriggerEnv[sparkwing.EnvGitHubEventName]
	if event == "" {
		event = "push"
	}
	action := run.TriggerEnv["GITHUB_ACTION"]
	if action == "" && event == sparkwing.EventPullRequest {
		action = run.TriggerEnv[sparkwing.EnvPRAction]
	}
	return githubAppSubscribes(sub, event, action, run.TriggerEnv, run.GitBranch)
}

// githubAppRerunIntake is a re-run of anchor, asked for by user.
func githubAppRerunIntake(anchor *store.Trigger, user string) githubAppIntake {
	env := make(map[string]string, len(anchor.TriggerEnv))
	for k, v := range anchor.TriggerEnv {
		if k != "GITHUB_DELIVERY" {
			env[k] = v
		}
	}
	var pr *sparkwing.PullRequest
	if env[sparkwing.EnvGitHubEventName] == sparkwing.EventPullRequest {
		pr = sparkwing.PullRequestFromEnv(env)
	}
	return githubAppIntake{user: user, branch: anchor.GitBranch, sha: anchor.GitSHA, env: env, at: time.Now(), prInfo: pr}
}
