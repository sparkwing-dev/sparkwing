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
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

const (
	webhookBodyLimit   = 25 << 20
	webhookMemoryLimit = 1 << 20
)

// EnvDefaultBranch carries the verified GitHub repository default branch.
const EnvDefaultBranch = "GITHUB_DEFAULT_BRANCH"

var defaultPullRequestActions = map[string]struct{}{
	"opened":      {},
	"synchronize": {},
	"reopened":    {},
}

type githubAppRepoRef struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

type githubAppDelivery struct {
	Action       string `json:"action"`
	Installation struct {
		ID    int64 `json:"id"`
		AppID int64 `json:"app_id"`
	} `json:"installation"`
	Repository githubAppRepoRef `json:"repository"`
}

type githubAppRun struct {
	Pipeline string `json:"pipeline"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}

type githubAppWebhookResp struct {
	Status string         `json:"status"`
	Reason string         `json:"reason,omitempty"`
	Runs   []githubAppRun `json:"runs,omitempty"`
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
	body, status, err := s.readSignedGitHubDelivery(w, r)
	if err != nil {
		if status == http.StatusInternalServerError {
			s.writeInternalError(w, r, "github app delivery spool", err)
			return
		}
		writeError(w, status, err)
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
	if err := s.operator.EnqueueGitHubDelivery(r.Context(), delivery, event, body, time.Now()); err != nil {
		if errors.Is(err, store.ErrGitHubDeliveryConflict) {
			writeError(w, http.StatusConflict, err)
		} else {
			s.writeInternalError(w, r, "queue github app delivery", err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, githubAppWebhookResp{Status: "queued"})
}

func (s *Server) processGitHubDelivery(w http.ResponseWriter, r *http.Request, queued store.GitHubDelivery) {
	body, event, delivery := queued.Body, queued.Event, queued.ID
	var env githubAppDelivery
	if err := json.Unmarshal(body, &env); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	switch event {
	case "ping":
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
	case "installation":
		s.handleGitHubAppInstallationEvent(w, r, env, delivery)
	case "installation_repositories":
		s.githubApp.forgetCovering()
		s.handleGitHubAppInstallationRepositoriesEvent(w, r, env, body)
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

const githubDeliverySpools = 4

// safety: a delivery past 1 MiB spools to disk while it is hashed, so an
// unsigned one never holds memory near the body limit; a few slots bound its disk.
func (s *Server) readSignedGitHubDelivery(w http.ResponseWriter, r *http.Request) (body []byte, status int, err error) {
	reader := http.MaxBytesReader(w, r.Body, webhookBodyLimit)
	mac := s.githubApp.client.WebhookMAC()
	head, err := io.ReadAll(io.LimitReader(reader, webhookMemoryLimit+1))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	_, _ = mac.Write(head)
	if len(head) <= webhookMemoryLimit {
		if !mac.Verify(r.Header.Get("X-Hub-Signature-256")) {
			return nil, http.StatusUnauthorized, errors.New("signature mismatch")
		}
		return head, http.StatusOK, nil
	}
	select {
	case s.deliverySpools <- struct{}{}:
		defer func() { <-s.deliverySpools }()
	default:
		w.Header().Set("Retry-After", "30")
		return nil, http.StatusServiceUnavailable, errors.New("too many large GitHub deliveries are being received; redeliver this one")
	}
	spool, err := os.CreateTemp("", "github-delivery-")
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	defer func() {
		if cleanup := errors.Join(spool.Close(), os.Remove(spool.Name())); cleanup != nil {
			body, status, err = nil, http.StatusInternalServerError, errors.Join(err, cleanup)
		}
	}()
	if _, err := spool.Write(head); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	rest, err := io.Copy(io.MultiWriter(mac, spool), reader)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, http.StatusRequestEntityTooLarge, err
	}
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if !mac.Verify(r.Header.Get("X-Hub-Signature-256")) {
		return nil, http.StatusUnauthorized, errors.New("signature mismatch")
	}
	body = make([]byte, int64(len(head))+rest)
	if _, err := spool.ReadAt(body, 0); err != nil {
		return nil, http.StatusInternalServerError, err
	}
	return body, http.StatusOK, nil
}

func (s *Server) handleGitHubAppInstallationRepositoriesEvent(w http.ResponseWriter, r *http.Request, env githubAppDelivery, body []byte) {
	ctx := r.Context()
	in, err := s.operator.GitHubAppInstallationTeam(ctx, env.Installation.ID)
	if errors.Is(err, store.ErrNotFound) {
		githubAppIgnored(w, "no team holds this installation")
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "github app installation repositories", err)
		return
	}
	tenant, err := s.tenantForTeam(ctx, in.Team)
	if err != nil {
		s.writeInternalError(w, r, "github app installation team", err)
		return
	}
	current, err := s.githubApp.client.InstallationRepositories(ctx, env.Installation.ID)
	if err != nil {
		if env.Action == "removed" {
			var removal struct {
				Removed []githubAppRepoRef `json:"repositories_removed"`
			}
			if decodeErr := json.Unmarshal(body, &removal); decodeErr != nil {
				writeError(w, http.StatusBadRequest, decodeErr)
				return
			}
			for _, removed := range removal.Removed {
				stillCovered := false
				for _, repo := range current {
					if repo.ID == removed.ID {
						stillCovered = true
						break
					}
				}
				if !stillCovered {
					if revokeErr := tenant.WithdrawGitHubAppAutomation(ctx, env.Installation.ID, removed.ID); revokeErr != nil {
						s.writeInternalError(w, r, "revoke unreadable removed repository automation", revokeErr)
						return
					}
				}
			}
			if !s.withdrawUnlistedGitHubRemoval(w, r, tenant, env.Installation.ID, body, current, err) {
				return
			}
		}
		writeError(w, http.StatusBadGateway, fmt.Errorf("read installation repositories: %w", err))
		return
	}
	covered := make([]int64, 0, len(current))
	for _, repo := range current {
		covered = append(covered, repo.ID)
	}
	if err := tenant.ReconcileGitHubInstallationRepositories(ctx, env.Installation.ID, covered); err != nil {
		s.writeInternalError(w, r, "withdraw removed GitHub repository automation and schedules", err)
		return
	}
	writeJSON(w, http.StatusOK, githubAppWebhookResp{Status: "updated"})
}

// safety: an unreadable listing withdraws the whole installation's schedules, but a listing cut only
// for its size withdraws just the repositories GitHub names as removed and keeps the rest; a named
// repository the cut listing still shows was added back after the removal, so it keeps its schedules.
func (s *Server) withdrawUnlistedGitHubRemoval(w http.ResponseWriter, r *http.Request, tenant *store.Tenant,
	installation int64, body []byte, listed []githubapp.Repository, listErr error,
) bool {
	if !errors.Is(listErr, githubapp.ErrTooManyRepositories) {
		if _, err := tenant.WithdrawGitHubCronInstallation(r.Context(), installation); err != nil {
			s.writeInternalError(w, r, "withdraw unreadable installation schedules", err)
			return false
		}
		return true
	}
	var p struct {
		Removed []githubAppRepoRef `json:"repositories_removed"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode installation_repositories payload: %w", err))
		return false
	}
	covered := make(map[int64]bool, len(listed))
	for _, repo := range listed {
		covered[repo.ID] = true
	}
	for _, repo := range p.Removed {
		if covered[repo.ID] {
			continue
		}
		if _, err := tenant.WithdrawGitHubCronRepository(r.Context(), installation, repo.ID); err != nil {
			s.writeInternalError(w, r, "withdraw removed GitHub repository schedule", err)
			return false
		}
	}
	return true
}

// safety: an installation event only removes or pauses a binding; creating one
// needs the connect flow, because GitHub's notice that the App was installed
// says nothing about which team should hold it.
func (s *Server) handleGitHubAppInstallationEvent(w http.ResponseWriter, r *http.Request, env githubAppDelivery, delivery string) {
	// safety: only installation events carry the full installation object, so
	// a body without this App's id is another event's or another App's.
	if env.Installation.AppID != s.githubApp.client.AppID() {
		githubAppIgnored(w, "the installation event names another App")
		return
	}
	id := env.Installation.ID
	op := s.operator
	var err error
	switch env.Action {
	case "deleted":
		var team store.Team
		team, err = op.UnbindGitHubAppInstallation(r.Context(), id)
		if err == nil {
			s.logger.Info("github app installation uninstalled", "team", string(team), "installation_id", id, "delivery", delivery)
		}
	case "suspend", "unsuspend":
		err = op.SetGitHubAppInstallationSuspended(r.Context(), id, env.Action == "suspend")
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
		UpdatedAt      string `json:"updated_at"`
		Merged         bool   `json:"merged"`
		MergeCommitSHA string `json:"merge_commit_sha"`
		Head           struct {
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
	at     time.Time
	prInfo *nodeproto.PullRequest
	// headRepo is GitHub's ID for the repository a pull request's head lives in.
	headRepo int64
}

// bug: GitHub push payloads use Unix seconds; other deliveries may use timestamps.
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
		"GITHUB_EVENT_NAME":      event,
		"GITHUB_ACTION":          env.Action,
		envGitHubAppInstallation: strconv.FormatInt(env.Installation.ID, 10),
		EnvDefaultBranch:         env.Repository.DefaultBranch,
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
		if (!isBranch && !isTag) || (isBranch && branch == "") || (isTag && tag == "") || !runspec.IsCommitID(p.After) {
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
		base["GITHUB_EVENT_NAME"] = githubEventPush
		if isTag {
			base["GITHUB_REF_TYPE"], base["GITHUB_TAG"] = "tag", tag
		} else {
			base["GITHUB_REF_TYPE"] = "branch"
		}
		return githubAppIntake{
			user: p.Pusher.Name, branch: branch, tag: tag, sha: p.After, env: base, at: githubPushedAt(p.Repository.PushedAt),
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
		// safety: an unreadable published_at leaves the time zero, and the caller
		// refuses an undated event.
		var at time.Time
		if parsed, err := time.Parse(time.RFC3339, p.Release.PublishedAt); err == nil {
			at = parsed
		}
		return githubAppIntake{user: p.Release.Author.Login, branch: p.Release.TagName, env: base, at: at}, "", nil
	}
	if event == "create" || event == "delete" {
		var p struct {
			Ref          string          `json:"ref"`
			RefType      string          `json:"ref_type"`
			MasterBranch json.RawMessage `json:"master_branch"`
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
		// safety: GitHub's delete payload has no master_branch and its create
		// payload always does, so a delete carrying one is a create body.
		if event == "delete" && p.MasterBranch != nil {
			return githubAppIntake{}, "a branch deletion carries no master_branch", nil
		}
		if event == "delete" && env.Repository.DefaultBranch == "" {
			return githubAppIntake{}, "branch deletion names no default branch", nil
		}
		base["GITHUB_REF"], base["GITHUB_REF_TYPE"] = "refs/heads/"+p.Ref, "branch"
		at := githubPushedAt(p.Repository.PushedAt)
		if updated := githubPushedAt(p.Repository.UpdatedAt); updated.After(at) {
			at = updated
		}
		branch := p.Ref
		if event == "delete" {
			branch = env.Repository.DefaultBranch
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
	if !sameRepoPullRequest(pr.Head.Repo, pr.Base.Repo) {
		return githubAppIntake{}, githubAppForkReason, nil
	}
	if !runspec.IsCommitID(pr.Head.SHA) {
		return githubAppIntake{}, "the pull request names no head commit", nil
	}
	// safety: a closed pull request's head is code nobody merged; only the merge
	// commit GitHub reports for a merged pull request may run, and it runs as the
	// base branch it landed on.
	sha := pr.Head.SHA
	if env.Action == "closed" {
		if !pr.Merged {
			return githubAppIntake{}, "an unmerged closed pull request starts nothing", nil
		}
		if !runspec.IsCommitID(pr.MergeCommitSHA) {
			return githubAppIntake{}, "the merged pull request names no merge commit", nil
		}
		sha = pr.MergeCommitSHA
	}
	base["GITHUB_EVENT_NAME"] = "pull_request"
	base["GITHUB_PR_NUMBER"] = strconv.Itoa(p.Number)
	base["GITHUB_PR_ACTION"] = env.Action
	base["GITHUB_BASE_REF"], base["GITHUB_BASE_SHA"] = pr.Base.Ref, pr.Base.SHA
	base["GITHUB_HEAD_REF"], base["GITHUB_HEAD_SHA"] = pr.Head.Ref, pr.Head.SHA
	base["GITHUB_REF"], base["GITHUB_REF_TYPE"] = "refs/pull/"+strconv.Itoa(p.Number)+"/head", "branch"
	if env.Action == "closed" {
		base["GITHUB_REF"] = "refs/heads/" + pr.Base.Ref
	}
	base["GITHUB_LABEL"] = p.Label.Name
	base["GITHUB_MERGED"] = strconv.FormatBool(pr.Merged)
	// safety: an unreadable updated_at leaves the time zero, and the caller
	// refuses an undated event.
	updated, err := time.Parse(time.RFC3339, pr.UpdatedAt)
	if err != nil {
		updated = time.Time{}
	}
	return githubAppIntake{
		user: pr.User.Login, branch: pr.Head.Ref, sha: sha, env: base, at: updated,
		prInfo: PullRequestForTrigger(base), headRepo: pr.Head.Repo.ID,
	}, "", nil
}

// PullRequestForTrigger projects authenticated trigger metadata into the SDK run context.
func PullRequestForTrigger(env map[string]string) *nodeproto.PullRequest {
	if env["GITHUB_EVENT_NAME"] != "pull_request" {
		return nil
	}
	number, _ := strconv.Atoi(env["GITHUB_PR_NUMBER"])
	return &nodeproto.PullRequest{
		Number: number, Action: env["GITHUB_PR_ACTION"],
		BaseRef: env["GITHUB_BASE_REF"], BaseSHA: env["GITHUB_BASE_SHA"],
		HeadRef: env["GITHUB_HEAD_REF"], HeadSHA: env["GITHUB_HEAD_SHA"],
	}
}

const githubAppForkReason = "pull requests from forks are not run"

// safety: a zero id names no repository, so two missing ids must not read as
// the same repository and admit a fork.
func sameRepoPullRequest(head, base *githubAppRepoRef) bool {
	return head != nil && base != nil && head.ID > 0 && head.ID == base.ID
}

// safety: A delayed GitHub clock may not make a valid delivery look older than its binding.
const githubAppClockSkew = time.Minute

func githubAppDeliveryDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// safety: Installation identity selects the owning team before a delivery may start work.
func (s *Server) githubAppBinding(w http.ResponseWriter, r *http.Request, env githubAppDelivery) (store.GitHubAppInstallation, *store.Tenant, store.GitHubRepo, bool) {
	ctx := r.Context()
	repo, ok := store.ParseGitHubRepo(env.Repository.FullName)
	if !ok || env.Repository.ID <= 0 || env.Installation.ID <= 0 {
		githubAppIgnored(w, "the delivery names no repository and installation")
		return store.GitHubAppInstallation{}, nil, store.GitHubRepo{}, false
	}
	repo.ID = env.Repository.ID
	in, err := s.operator.GitHubAppInstallationTeam(ctx, env.Installation.ID)
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
	// safety: completed and failed digests expire after the receipt-based replay window.
	// Compare event time with receipt so queued work never ages out while pending.
	if received, ok := r.Context().Value(githubDeliveryReceivedKey{}).(time.Time); ok && received.Sub(intake.at) > store.GitHubAppDeliveryRetention-githubAppClockSkew {
		s.logger.Warn("github app delivery refused: older than the replay window", "team", string(in.Team),
			"event", event, "repo", repo.Slug(), "event_at", intake.at.UTC(), "delivery", delivery)
		writeError(w, http.StatusConflict, errors.New("the event is older than the delivery replay window"))
		return
	}
	if intake.at.Add(githubAppClockSkew).Before(in.CreatedAt) {
		githubAppIgnored(w, "the event predates this team's connection of the installation")
		return
	}
	var cronStatus int
	var cronErr error
	var cronSkip string
	if event == "push" && intake.branch != "" && intake.branch == env.Repository.DefaultBranch {
		cronStatus, cronSkip, cronErr = s.reconcileGitHubAppCrons(r, in, tenant, repo, intake)
		if cronErr != nil {
			apierr.NoteResponseError(w, cronErr)
			s.logger.Warn("github app schedule reconciliation failed", "team", string(in.Team), "repo", repo.Slug(), "err", cronErr)
		}
	}
	subs, err := tenant.GitHubAppTriggersFor(ctx, env.Installation.ID, env.Repository.ID)
	if err != nil {
		s.writeInternalError(w, r, "github app triggers", err)
		return
	}
	var planned []githubAppPlannedRun
	manualPipelines := make(map[string]bool, len(subs))
	for _, sub := range subs {
		manualPipelines[sub.Pipeline] = true
		if githubAppSubscribes(sub, event, env.Action, intake.env, intake.branch) {
			planned = append(planned, githubAppPlannedRun{pipeline: sub.Pipeline, intake: intake, subscription: &sub})
		}
	}
	if grant, err := tenant.GitHubAppAutomation(ctx, in.InstallationID, repo.ID); err == nil && (event == "pull_request" || event == "push" && intake.branch != "") {
		// safety: a delivery from before consent must not gain permission by being replayed later.
		if !intake.at.Add(githubAppClockSkew).Before(grant.EnabledAt) {
			preview, err := s.githubAppAutomationPreview(ctx, tenant, in, repo)
			if err != nil || (preview.Status != "ready" && preview.Status != "missing") {
				if err != nil {
					apierr.NoteResponseError(w, err)
				} else {
					apierr.NoteResponseError(w, errors.New(preview.Error))
				}
				s.logger.Warn("github app repository automation unavailable", "team", string(in.Team), "repo", repo.Slug(), "status", preview.Status, "error", preview.Error, "err", err)
				if len(planned) == 0 {
					if err != nil {
						s.writeInternalError(w, r, "discover repository automation", err)
					} else if preview.Status == "unavailable" {
						writeJSON(w, http.StatusBadGateway, preview)
					} else {
						writeJSON(w, http.StatusUnprocessableEntity, preview)
					}
					return
				}
			} else if preview.Enabled {
				for _, decl := range preview.Pipelines {
					// safety: an existing subscription owns its pipeline's event policy, including events it excludes.
					if !manualPipelines[decl.Pipeline] && automationSubscribes(decl, event, env.Action, intake) {
						planned = append(planned, githubAppPlannedRun{pipeline: decl.Pipeline, intake: intake, automation: &grant})
					}
				}
			}
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeInternalError(w, r, "read repository automation consent", err)
		return
	}
	if len(planned) == 0 {
		if cronErr != nil {
			writeError(w, cronStatus, cronErr)
			return
		}
		if cronSkip != "" {
			githubAppIgnored(w, cronSkip)
			return
		}
		githubAppIgnored(w, "no pipeline of this team subscribes to "+event+" on "+repo.Slug()+" for this branch")
		return
	}
	s.startGitHubAppRuns(w, r, in, tenant, env, repo, event, delivery, body, planned)
}

func (s *Server) reconcileGitHubAppCrons(r *http.Request, in store.GitHubAppInstallation,
	tenant *store.Tenant, repo store.GitHubRepo, intake githubAppIntake,
) (int, string, error) {
	publication, replay, err := tenant.PrepareGitHubAppPublication(r.Context(), in.InstallationID, repo.ID)
	if errors.Is(err, store.ErrGitHubAppAutomationRevoked) {
		return 0, "repository automation consent is not current for this delivery", nil
	}
	if err != nil {
		return http.StatusConflict, "", err
	}
	if replay {
		_, _, err := tenant.ReplaceGitHubAppSchedules(r.Context(), publication, "https://github.com/"+repo.Slug()+".git", nil, nil)
		if err != nil {
			return http.StatusInternalServerError, "", err
		}
		return 0, "", nil
	}
	grant, err := tenant.GitHubAppAutomation(r.Context(), in.InstallationID, repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, "repository automation consent is not enabled", nil
	}
	if err != nil {
		return http.StatusInternalServerError, "", err
	}
	if intake.at.Add(githubAppClockSkew).Before(grant.EnabledAt) {
		return 0, "the push predates repository automation consent", nil
	}
	snapshot, err := s.githubApp.client.FileAtDefaultHead(r.Context(), in.InstallationID, repo.Owner, repo.Name,
		".sparkwing/"+projectconfig.Filename)
	if err != nil {
		return http.StatusBadGateway, "", fmt.Errorf("read GitHub schedule config: %w", err)
	}
	if snapshot.RepositoryID != repo.ID || snapshot.DefaultBranch != intake.branch || snapshot.HeadSHA != intake.sha {
		return 0, "the push no longer names this repository's default-branch head", nil
	}
	var entries []crons.Declared
	if snapshot.Found {
		cfg, err := projectconfig.Parse(snapshot.Content)
		if err != nil {
			return http.StatusUnprocessableEntity, "", fmt.Errorf("read GitHub schedule config: %w", err)
		}
		for _, pipeline := range cfg.Registry().Pipelines {
			for _, schedule := range pipeline.On.Schedule {
				if schedule.Where == pipelines.ScheduleWhereController {
					entries = append(entries, crons.Declared{Pipeline: pipeline.Name, Name: schedule.EffectiveName(), Trigger: schedule})
				}
			}
		}
	}
	ids, withdrawn, err := s.armGitHubAppSchedules(r.Context(), tenant, in.InstallationID, publication, repo, intake.branch, entries)
	if err != nil {
		if errors.Is(err, store.ErrCronScheduleTaken) || errors.Is(err, store.ErrGitHubAppAutomationRevoked) || errors.Is(err, store.ErrGitHubPublicationStale) {
			return http.StatusConflict, "", err
		}
		var refusal *apierr.Error
		if errors.As(err, &refusal) {
			return apierr.Status(refusal.Code), "", err
		}
		var minimum *store.ComputeLimitError
		if errors.As(err, &minimum) || errors.Is(err, store.ErrInvalidInput) {
			return http.StatusUnprocessableEntity, "", err
		}
		return http.StatusInternalServerError, "", fmt.Errorf("arm GitHub App schedules: %w", err)
	}
	s.logger.Info("github app schedules reconciled", "team", string(in.Team), "repo", repo.Slug(),
		"sha", intake.sha, "declared", len(ids), "withdrawn", len(withdrawn))
	return 0, "", nil
}

func (s *Server) handleGitHubAppRepositoryEvent(w http.ResponseWriter, r *http.Request, env githubAppDelivery) {
	if env.Action != "renamed" && env.Action != "transferred" && env.Action != "deleted" {
		githubAppIgnored(w, "repository action starts nothing")
		return
	}
	_, tenant, repo, ok := s.githubAppBinding(w, r, env)
	if !ok {
		return
	}
	if env.Action == "deleted" {
		if err := tenant.WithdrawGitHubAppAutomation(r.Context(), env.Installation.ID, repo.ID); err != nil {
			s.writeInternalError(w, r, "revoke deleted repository automation", err)
			return
		}
		if _, err := tenant.WithdrawGitHubCronRepository(r.Context(), env.Installation.ID, repo.ID); err != nil {
			s.writeInternalError(w, r, "withdraw deleted GitHub repository schedule", err)
			return
		}
		writeJSON(w, http.StatusOK, githubAppWebhookResp{Status: "updated"})
		return
	}
	inst, covered, err := s.githubApp.liveCoveringInstallation(r.Context(), repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !covered || inst.ID != env.Installation.ID || inst.SuspendedAt != nil {
		if env.Action == "transferred" {
			if err := tenant.WithdrawGitHubAppAutomation(r.Context(), env.Installation.ID, repo.ID); err != nil {
				s.writeInternalError(w, r, "revoke transferred repository automation", err)
				return
			}
			if _, err := tenant.WithdrawGitHubCronRepository(r.Context(), env.Installation.ID, repo.ID); err != nil {
				s.writeInternalError(w, r, "withdraw transferred GitHub repository schedule", err)
				return
			}
		}
		githubAppIgnored(w, "the installation no longer covers "+repo.Slug())
		return
	}
	repositories, err := s.githubApp.client.InstallationRepositories(r.Context(), env.Installation.ID)
	truncated := errors.Is(err, githubapp.ErrTooManyRepositories)
	if err != nil && !truncated {
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
	if !matched && truncated {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !matched {
		githubAppIgnored(w, "the installation no longer covers repository id")
		return
	}
	if err := tenant.RenameGitHubAppAutomation(r.Context(), env.Installation.ID, repo.ID, repo.Slug()); err != nil {
		s.writeInternalError(w, r, "rename repository automation", err)
		return
	}
	if _, err := tenant.WithdrawOtherGitHubCronInstallations(r.Context(), env.Installation.ID, repo.ID); err != nil {
		s.writeInternalError(w, r, "withdraw former GitHub repository schedule", err)
		return
	}
	if err := tenant.RenameGitHubCronRepository(r.Context(), env.Installation.ID, repo.ID,
		"https://github.com/"+repo.Slug()+".git"); err != nil {
		if errors.Is(err, store.ErrCronScheduleTaken) {
			if _, werr := tenant.WithdrawGitHubCronRepository(r.Context(), env.Installation.ID, repo.ID); werr != nil {
				s.writeInternalError(w, r, "withdraw conflicting GitHub repository schedule", werr)
				return
			}
			writeError(w, http.StatusConflict, err)
			return
		}
		s.writeInternalError(w, r, "rename GitHub repository schedule", err)
		return
	}
	if err := tenant.RenameGitHubAppTriggerRepository(r.Context(), env.Installation.ID, repo.ID, repo.Slug()); err != nil {
		s.writeInternalError(w, r, "update github app repository", err)
		return
	}
	writeJSON(w, http.StatusOK, githubAppWebhookResp{Status: "updated"})
}

// safety: Branch filters gate every event that runs branch code; tag patterns alone select tag
// pushes and gate releases, because anyone who can push a tag can publish a release on it.
func githubAppSubscribes(sub store.GitHubAppTrigger, event, action string, env map[string]string, branch string) bool {
	baseMatches := runspec.BranchSelector(sub.BaseBranches).Matches(env["GITHUB_BASE_REF"])
	switch event {
	case "push":
		if env["GITHUB_REF_TYPE"] == "tag" {
			return githubTagMatches(sub.Tags, env["GITHUB_TAG"])
		}
		return sub.Push && runspec.BranchSelector(sub.Branches).Matches(branch)
	case "pull_request":
		switch action {
		case "opened", "synchronize", "reopened":
			return sub.PullRequest && baseMatches
		case "closed":
			return sub.PullRequestClosed && baseMatches
		case "ready_for_review":
			return sub.PullRequestReadyForReview && baseMatches
		case "labeled":
			if !sub.PullRequestLabeled || !baseMatches {
				return false
			}
			for _, want := range sub.PullRequestLabels {
				if strings.EqualFold(want, env["GITHUB_LABEL"]) {
					return true
				}
			}
		}
	case "release":
		return (action == "published" && sub.ReleasePublished || action == "prereleased" && sub.ReleasePrereleased) && githubTagMatches(sub.ReleaseTags, env["GITHUB_TAG_NAME"])
	case "create":
		return sub.BranchCreate && runspec.BranchSelector(sub.Branches).Matches(env["GITHUB_REF"])
	case "delete":
		return sub.BranchDelete && runspec.BranchSelector(sub.Branches).Matches(env["GITHUB_REF"])
	}
	return false
}

type githubAppPlannedRun struct {
	pipeline     string
	subscription *store.GitHubAppTrigger
	intake       githubAppIntake
	automation   *store.GitHubAppAutomation
}

// safety: A signed App delivery starts its budgeted run set only once.
func (s *Server) startGitHubAppRuns(w http.ResponseWriter, r *http.Request, in store.GitHubAppInstallation,
	tenant *store.Tenant, env githubAppDelivery, repo store.GitHubRepo, event, delivery string, body []byte,
	planned []githubAppPlannedRun,
) {
	ctx := r.Context()
	// safety: GitHub is asked on every delivery rather than through the cache
	// the token route keeps, because a cached answer lags a removal that no
	// installation_repositories delivery has reached this replica for yet.
	gh, covered, err := s.githubApp.liveCoveringInstallation(ctx, repo)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("GitHub installation lookup: %w", err))
		return
	}
	if !covered || gh.ID != env.Installation.ID || gh.SuspendedAt != nil {
		githubAppIgnored(w, "the installation no longer covers "+repo.Slug())
		return
	}
	if event == "release" || event == "create" || event == "delete" {
		sha := ""
		for _, plan := range planned {
			existing, err := tenant.FindGitHubAppTrigger(ctx, githubAppDeliveryDigest(body), plan.pipeline)
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
	failed := 0
	for _, plan := range planned {
		// safety: a run this delivery already started answers without spending,
		// so manually processing a partial delivery again spends only for missing runs.
		if existing, err := tenant.FindGitHubAppTrigger(ctx, githubAppDeliveryDigest(body), plan.pipeline); err == nil && existing != nil {
			resp.Runs = append(resp.Runs, githubAppRun{Pipeline: plan.pipeline, RunID: existing.ID, Status: "duplicate"})
			continue
		}
		run, err := s.startGitHubAppRun(r, tenant, in, plan, repo, body)
		if err != nil {
			var refusal *apierr.Error
			if errors.As(err, &refusal) && refusal.Details["retry_after_ms"] != nil {
				if !githubAppStartedAny(resp.Runs) {
					routepolicy.WritePolicyError(w, err)
					return
				}
				failed++
				resp.Runs = append(resp.Runs, githubAppRun{Pipeline: plan.pipeline, Status: "failed", Reason: err.Error()})
				continue
			}
			if s.writeComputeLimitRefusal(w, r, err) {
				return
			}
			writeIdentityError(w, s, r, "github app run", err)
			return
		}
		resp.Runs = append(resp.Runs, run)
	}
	if !githubAppStartedAny(resp.Runs) && failed == 0 {
		resp.Status, resp.Reason = "ignored", "repository automation consent is no longer current"
	}
	s.logger.Info("github app delivery accepted", "team", string(in.Team), "event", event,
		"repo", repo.Slug(), "sha", planned[0].intake.sha, "runs", len(resp.Runs), "failed", failed, "delivery", delivery)
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

func (s *Server) startGitHubAppRun(
	r *http.Request, tenant *store.Tenant, installation store.GitHubAppInstallation, plan githubAppPlannedRun, repo store.GitHubRepo, body []byte,
) (githubAppRun, error) {
	in := plan.intake
	now := time.Now()
	ref := in.env["GITHUB_REF"]
	key := runspec.RepoKey(fmt.Sprintf("gh:%d", repo.ID))
	defaultBranch := in.env[EnvDefaultBranch]
	request := runspec.Request{
		Version: 1, Team: string(tenant.Team()), Pipeline: plan.pipeline, Mode: runspec.IntakeExecute, Origin: runspec.OriginApp,
		Actor: runspec.Actor{Kind: "github", ID: in.user, Display: in.user},
		Git: runspec.Git{
			Repository: runspec.Repository{Key: key, Display: repo.Slug()}, Ref: ref, SHA: in.sha,
			DefaultBranch: runspec.Optional[string]{Set: defaultBranch != "", Value: defaultBranch},
			App:           runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{Event: in.env["GITHUB_EVENT_NAME"], Action: in.env["GITHUB_ACTION"], DeliveryKey: githubAppDeliveryDigest(body), InstallationID: installation.InstallationID, RepositoryID: repo.ID, Ref: ref, SHA: in.sha, DefaultBranch: defaultBranch, DefaultBranchPush: in.env["GITHUB_EVENT_NAME"] == "push" && ref == "refs/heads/"+defaultBranch, VerifiedAt: now}},
		},
		Source: runspec.Source{Location: runspec.SourceLocation{RemoteURL: "https://github.com/" + repo.Slug() + ".git", PipelineDir: ".sparkwing"}},
	}
	git, code, err := s.selectIntakeSource(r.Context(), tenant.Team(), request, func(ctx context.Context, sha, name string) ([]byte, bool, error) {
		return s.githubApp.client.FileAtCommit(ctx, installation.InstallationID, repo.ID, repo.Owner, repo.Name, sha, name)
	})
	if refusal := (*apierr.Error)(nil); errors.As(err, &refusal) && refusal.Code == apierr.InvalidArgs {
		return githubAppRun{Pipeline: plan.pipeline, Status: "refused", Reason: err.Error()}, nil
	}
	if err != nil {
		return githubAppRun{}, err
	}
	request.Git, request.Source = git, code
	if in.prInfo != nil {
		pr := in.prInfo
		head := runspec.RepoKey(fmt.Sprintf("gh:%d", in.headRepo))
		request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: int64(pr.Number), Action: pr.Action, BaseRepo: key, HeadRepo: head, Fork: head != key, BaseRef: pr.BaseRef, BaseSHA: pr.BaseSHA, HeadRef: pr.HeadRef, HeadSHA: pr.HeadSHA, Label: in.env["GITHUB_LABEL"], Merged: runspec.Optional[bool]{Set: in.env["GITHUB_MERGED"] != "", Value: in.env["GITHUB_MERGED"] == "true"}}}
	}
	result, err := admitWithFlood(r.Context(), s, func(ctx context.Context) (store.AdmissionResult, error) {
		return tenant.AdmitGitHubAppRun(ctx, store.GitHubAppAdmission{Request: request, Installation: installation, Subscription: plan.subscription, Automation: plan.automation, Event: r.Header.Get("X-GitHub-Event")})
	})
	if errors.Is(err, store.ErrGitHubAppAutomationRevoked) {
		return githubAppRun{Pipeline: plan.pipeline, Status: "ignored"}, nil
	}
	if err != nil {
		return githubAppRun{}, fmt.Errorf("admit App run: %w", err)
	}
	status := "dispatched"
	if result.Replayed {
		status = "duplicate"
	} else {
		s.recordQueueActivity(now)
	}
	return githubAppRun{Pipeline: plan.pipeline, RunID: result.RunID, Status: status}, nil
}
