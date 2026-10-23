package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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

// safety: A suite rerequest starts only subscribed pipelines with a prior App run on that commit;
// that run supplies branch and pull-request context.
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
	if !runspec.IsCommitID(sha) {
		githubAppIgnored(w, "the "+event+" names no head commit")
		return
	}
	in, tenant, repo, ok := s.githubAppBinding(w, r, env)
	if !ok {
		return
	}
	for _, pr := range prs {
		if !sameRepoPullRequest(pr.Head.Repo, pr.Base.Repo) {
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
	runs, err := tenant.GitHubCommitRuns(r.Context(), repo, sha)
	if err != nil {
		s.writeInternalError(w, r, "github app commit runs", err)
		return
	}
	var anchors []*store.RunRow
	for i := range runs {
		run := &runs[i]
		if app := run.Request.Request.Git.App; app.Set && app.Value.InstallationID == env.Installation.ID && run.Request.Request.Git.SHA == sha {
			anchors = append(anchors, run)
		}
	}
	var planned []githubAppPlannedRun
	if event == "check_run" {
		pipeline, named := strings.CutPrefix(p.CheckRun.Name, "sparkwing/")
		for _, run := range anchors {
			if sub, ok := subscribed[pipeline]; named && ok && run.ID == p.CheckRun.ExternalID && run.Pipeline == pipeline && githubAppRunEventSubscribed(run, sub) {
				planned = append(planned, githubAppPlannedRun{pipeline: pipeline, intake: githubAppRerunIntake(run, p.Sender.Login), subscription: &sub})
				break
			}
		}
	} else {
		for _, sub := range subs {
			if !sub.Push && !sub.PullRequest && len(sub.Tags) == 0 {
				continue
			}
			if run := githubAppRerunAnchor(anchors, sub); run != nil {
				planned = append(planned, githubAppPlannedRun{pipeline: sub.Pipeline, intake: githubAppRerunIntake(run, p.Sender.Login), subscription: &sub})
			}
		}
	}
	if len(planned) == 0 {
		githubAppIgnored(w, "no run of a subscribed pipeline of this team at "+sha+" to re-run")
		return
	}
	s.startGitHubAppRuns(w, r, in, tenant, env, repo, event, delivery, body, planned)
}

func githubAppRerunAnchor(anchors []*store.RunRow, sub store.GitHubAppTrigger) *store.RunRow {
	for _, run := range anchors {
		if run.Pipeline == sub.Pipeline && githubAppRunEventSubscribed(run, sub) {
			return run
		}
	}
	return nil
}

func githubAppRunEventSubscribed(run *store.RunRow, sub store.GitHubAppTrigger) bool {
	in := githubAppRerunIntake(run, "")
	return githubAppSubscribes(sub, in.env["GITHUB_EVENT_NAME"], in.env["GITHUB_ACTION"], in.env, in.branch)
}

func githubAppRerunIntake(anchor *store.RunRow, user string) githubAppIntake {
	request := anchor.Request.Request
	app := request.Git.App.Value
	env := map[string]string{"GITHUB_EVENT_NAME": app.Event, "GITHUB_ACTION": app.Action, "GITHUB_REF": request.Git.Ref, EnvDefaultBranch: app.DefaultBranch}
	branch := strings.TrimPrefix(request.Git.Ref, "refs/heads/")
	if strings.HasPrefix(request.Git.Ref, "refs/tags/") {
		env["GITHUB_REF_TYPE"], branch = "tag", ""
		env["GITHUB_TAG"] = strings.TrimPrefix(request.Git.Ref, "refs/tags/")
		if app.Event == "release" {
			env["GITHUB_TAG_NAME"] = env["GITHUB_TAG"]
		}
	} else {
		env["GITHUB_REF_TYPE"] = "branch"
	}
	var pr *nodeproto.PullRequest
	var head int64
	if request.PullRequest.Set {
		p := request.PullRequest.Value
		pr = &nodeproto.PullRequest{Number: int(p.Number), Action: p.Action, BaseRef: p.BaseRef, BaseSHA: p.BaseSHA, HeadRef: p.HeadRef, HeadSHA: p.HeadSHA}
		branch, env["GITHUB_BASE_REF"] = p.HeadRef, p.BaseRef
		if id, ok := strings.CutPrefix(string(p.HeadRepo), "gh:"); ok {
			if parsed, err := strconv.ParseInt(id, 10, 64); err == nil {
				head = parsed
			}
		}
		env["GITHUB_LABEL"] = p.Label
		if p.Merged.Set {
			env["GITHUB_MERGED"] = fmt.Sprint(p.Merged.Value)
		}
	}
	return githubAppIntake{user: user, branch: branch, sha: request.Git.SHA, env: env, at: time.Now(), prInfo: pr, headRepo: head}
}
