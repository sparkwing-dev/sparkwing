package controller

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: a submitted trigger cannot carry this key, because sanitizeTriggerEnv drops
// every key outside submittedTriggerEnvKeys.
const envGitHubAppInstallation = "GITHUB_APP_INSTALLATION_ID"

// safety: a connect flow is a few redirects long; a state that outlives that
// is one someone kept, not one a browser is using.
const githubAppStateTTL = 10 * time.Minute

type githubAppState struct {
	client   *githubapp.Client
	stateKey []byte
	statuses *githubCommitStatusReporter

	// safety: GitHub's answer for which installation covers a repository is
	// read on every run and token, so it is kept briefly rather than asked
	// for each time.
	mu       sync.Mutex
	covering map[string]coveringEntry
	sources  map[string]*sourceTokenEntry
}

type coveringEntry struct {
	inst    githubapp.Installation
	missing bool
	until   time.Time
}

// WithGitHubApp enables the deployment's GitHub App. The client holds the
// App's private key; the controller hands out only tokens restricted to one
// repository.
func (s *Server) WithGitHubApp(cfg githubapp.Config) *Server {
	client := githubapp.New(cfg)
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = githubapp.GitHubAPIURL
	}
	httpClient := cfg.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: githubStatusTimeout}
	}
	app := &githubAppState{
		client: client, stateKey: client.StateKey(),
		covering: map[string]coveringEntry{}, sources: map[string]*sourceTokenEntry{},
	}
	app.statuses = newGitHubCommitStatusReporter("", s.dashboardURL, apiURL, httpClient)
	app.statuses.tokenFor = func(ctx context.Context, st githubCommitStatus) (string, error) {
		tok, err := client.InstallationToken(ctx, st.Installation, []string{st.Repo}, map[string]string{"statuses": "write"})
		return tok.Token, err
	}
	s.githubApp = app
	return s
}

func (s *Server) githubAppStatusReporter() *githubCommitStatusReporter {
	if s.githubApp == nil {
		return nil
	}
	return s.githubApp.statuses
}

func (s *Server) githubAppEnabled(w http.ResponseWriter) bool {
	if s.githubApp != nil {
		return true
	}
	writeError(w, http.StatusNotFound, errors.New("this controller has no GitHub App configured"))
	return false
}

type githubConnectState struct {
	Team     string `json:"t"`
	Account  string `json:"a"`
	Expires  int64  `json:"e"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

func verifierDigest(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (a *githubAppState) signState(st githubConnectState) (string, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, a.stateKey)
	mac.Write(payload)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

var errConnectState = errors.New("the connect flow's state is not valid; start connecting again")

// safety: the state proves this controller started the flow for this account
// and team, and the verifier proves this browser is the one that started it;
// the caller records the nonce so a state finishes one flow only.
func (a *githubAppState) openState(raw, verifier string, p *Principal, now time.Time) (githubConnectState, error) {
	enc := base64.RawURLEncoding
	body, sig, ok := strings.Cut(raw, ".")
	if !ok {
		return githubConnectState{}, errConnectState
	}
	payload, err := enc.DecodeString(body)
	if err != nil {
		return githubConnectState{}, errConnectState
	}
	got, err := enc.DecodeString(sig)
	if err != nil {
		return githubConnectState{}, errConnectState
	}
	mac := hmac.New(sha256.New, a.stateKey)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return githubConnectState{}, errConnectState
	}
	var st githubConnectState
	if err := json.Unmarshal(payload, &st); err != nil {
		return githubConnectState{}, errConnectState
	}
	switch {
	case now.Unix() >= st.Expires:
		return githubConnectState{}, errors.New("the connect flow expired; start connecting again")
	case st.Account != p.AccountID || store.Team(st.Team) != p.Team:
		return githubConnectState{}, errors.New("the connect flow was started by another account or for another team")
	case verifier == "" || !hmac.Equal([]byte(st.Verifier), []byte(verifierDigest(verifier))):
		return githubConnectState{}, errors.New("the connect flow was started in another browser")
	}
	return st, nil
}

type githubAppConnectReq struct {
	RedirectURI string `json:"redirect_uri"`
}

type githubAppConnectResp struct {
	InstallURL   string `json:"install_url"`
	AuthorizeURL string `json:"authorize_url"`
	State        string `json:"state"`
	Verifier     string `json:"verifier"`
}

// safety: only an owner connects, and only an account with a linked GitHub
// identity, because the identity is what the user GitHub reports is held to.
func (s *Server) handleGitHubAppConnect(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	p, _, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req githubAppConnectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !s.redirectAllowed(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, errors.New("redirect_uri is not on this controller's allowlist"))
		return
	}
	if _, err := s.store.AccountIdentity(r.Context(), p.AccountID, store.ProviderGitHub); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusForbidden, errNoGitHubIdentity)
			return
		}
		s.writeInternalError(w, r, "github app identity", err)
		return
	}
	nonce, err := randomURLToken()
	if err != nil {
		s.writeInternalError(w, r, "github app connect", err)
		return
	}
	verifier, err := randomURLToken()
	if err != nil {
		s.writeInternalError(w, r, "github app connect", err)
		return
	}
	state, err := s.githubApp.signState(githubConnectState{
		Team: string(p.Team), Account: p.AccountID, Expires: time.Now().Add(githubAppStateTTL).Unix(),
		Nonce: nonce, Verifier: verifierDigest(verifier),
	})
	if err != nil {
		s.writeInternalError(w, r, "github app connect", err)
		return
	}
	writeJSON(w, http.StatusOK, githubAppConnectResp{
		InstallURL:   s.githubApp.client.InstallURL(state),
		AuthorizeURL: s.githubApp.client.AuthorizeURL(state, verifier, req.RedirectURI),
		State:        state,
		Verifier:     verifier,
	})
}

var errNoGitHubIdentity = errors.New("link a GitHub sign-in to this account before connecting the GitHub App")

type githubAppCompleteReq struct {
	State          string `json:"state"`
	Verifier       string `json:"verifier"`
	Code           string `json:"code"`
	InstallationID int64  `json:"installation_id"`
	RedirectURI    string `json:"redirect_uri"`
}

type githubAppInstallationJSON struct {
	InstallationID int64  `json:"installation_id"`
	AccountID      int64  `json:"account_id"`
	AccountLogin   string `json:"account_login"`
	AccountType    string `json:"account_type"`
	Suspended      bool   `json:"suspended"`
	ConnectedBy    string `json:"connected_by"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	ManageURL      string `json:"manage_url"`
}

func githubAppInstallationOut(in store.GitHubAppInstallation) githubAppInstallationJSON {
	manage := "https://github.com/settings/installations/" + strconv.FormatInt(in.InstallationID, 10)
	if in.AccountType == "Organization" {
		manage = "https://github.com/organizations/" + url.PathEscape(in.AccountLogin) +
			"/settings/installations/" + strconv.FormatInt(in.InstallationID, 10)
	}
	return githubAppInstallationJSON{
		InstallationID: in.InstallationID, AccountID: in.AccountID, AccountLogin: in.AccountLogin,
		AccountType: in.AccountType, Suspended: in.Suspended, ConnectedBy: in.ConnectedBy,
		CreatedAt: in.CreatedAt.Unix(), UpdatedAt: in.UpdatedAt.Unix(), ManageURL: manage,
	}
}

var errGitHubUserMismatch = errors.New("the GitHub user who authorized is not the one linked to your account")

var errGitHubInstallationAccess = errors.New("your GitHub account does not administer the account this installation belongs to")

// safety: seeing an installation is not enough to bind it, because an
// organization member with read access to one repository sees the
// organization's installation; binding needs the account's own user or an
// organization admin, which is who GitHub lets install the App.
func (s *Server) handleGitHubAppConnectComplete(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req githubAppCompleteReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Code == "" || req.InstallationID <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("code and installation_id are required"))
		return
	}
	if !s.redirectAllowed(req.RedirectURI) {
		writeError(w, http.StatusBadRequest, errors.New("redirect_uri is not on this controller's allowlist"))
		return
	}
	now := time.Now()
	st, err := s.githubApp.openState(req.State, req.Verifier, p, now)
	if err == nil {
		// safety: the used state is recorded in the store, so no replica and no
		// restart finishes the same flow twice.
		fresh, cerr := s.store.ConsumeGitHubAppConnectState(r.Context(), st.Nonce, time.Unix(st.Expires, 0), now)
		if cerr != nil {
			s.writeInternalError(w, r, "github app connect state", cerr)
			return
		}
		if !fresh {
			err = errors.New("the connect flow was already used; start connecting again")
		}
	}
	if err != nil {
		s.logger.Info("github app connect refused", "team", string(p.Team), "account", p.AccountID, "reason", err.Error())
		writeError(w, http.StatusForbidden, err)
		return
	}
	subject, err := s.store.AccountIdentity(r.Context(), p.AccountID, store.ProviderGitHub)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusForbidden, errNoGitHubIdentity)
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "github app identity", err)
		return
	}
	var inst githubapp.Installation
	check := func(ctx context.Context, userToken string, u githubapp.User) error {
		if strconv.FormatInt(u.ID, 10) != subject {
			return errGitHubUserMismatch
		}
		got, err := s.githubApp.client.Installation(ctx, req.InstallationID)
		if err != nil {
			return err
		}
		switch got.Account.Type {
		case "User":
			if got.Account.ID != u.ID {
				return errGitHubInstallationAccess
			}
		case "Organization":
			m, err := s.githubApp.client.UserOrgMembership(ctx, userToken, got.Account.Login)
			if err != nil {
				return err
			}
			if !m.Admin() {
				return errGitHubInstallationAccess
			}
		default:
			return errGitHubInstallationAccess
		}
		inst = got
		return nil
	}
	user, err := s.githubApp.client.ExchangeUserCode(r.Context(), req.Code, req.Verifier, req.RedirectURI, check)
	if err != nil {
		s.writeGitHubAppConnectError(w, r, p, req.InstallationID, err)
		return
	}
	bound, err := t.BindGitHubAppInstallation(r.Context(), store.GitHubAppInstallation{
		InstallationID: inst.ID, AccountID: inst.Account.ID, AccountLogin: inst.Account.Login,
		AccountType: inst.Account.Type, Suspended: inst.SuspendedAt != nil,
		ConnectedBy: p.AccountID, GitHubUserID: user.ID,
	}, time.Now())
	if errors.Is(err, store.ErrInstallationBoundElsewhere) {
		writeError(w, http.StatusConflict, errors.New(
			"this installation is connected to another team; its owner disconnects it, or the operator moves it"))
		return
	}
	if err != nil {
		writeIdentityError(w, s, r, "github app bind", err)
		return
	}
	s.logger.Info("github app installation connected", "team", string(p.Team), "installation_id", inst.ID,
		"account", inst.Account.Login, "account_type", inst.Account.Type, "by", p.AccountID, "github_user", user.ID)
	writeJSON(w, http.StatusCreated, githubAppInstallationOut(bound))
}

func (s *Server) writeGitHubAppConnectError(w http.ResponseWriter, r *http.Request, p *Principal, installation int64, err error) {
	s.logger.Info("github app connect refused", "team", string(p.Team), "account", p.AccountID,
		"installation_id", installation, "reason", err.Error())
	switch {
	case errors.Is(err, githubapp.ErrNotInstalled):
		writeError(w, http.StatusNotFound, errors.New("GitHub reports no installation of the App by that id"))
	case errors.Is(err, githubapp.ErrRejected):
		writeError(w, http.StatusForbidden, errors.New("GitHub did not confirm the authorization; start connecting again"))
	case errors.Is(err, errGitHubInstallationAccess):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, errGitHubUserMismatch):
		writeError(w, http.StatusForbidden, err)
	default:
		s.logger.Warn("github app connect failed", "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not be reached to finish connecting"))
	}
}

type githubAppResp struct {
	Slug          string                      `json:"slug"`
	Installations []githubAppInstallationJSON `json:"installations"`
}

func (s *Server) handleGitHubAppShow(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	list, err := t.GitHubAppInstallations(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list github app installations", err)
		return
	}
	out := githubAppResp{Slug: s.githubApp.client.Slug(), Installations: []githubAppInstallationJSON{}}
	for _, in := range list {
		out.Installations = append(out.Installations, githubAppInstallationOut(in))
	}
	writeJSON(w, http.StatusOK, out)
}

func installationIDFrom(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("installation_id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("installation_id is GitHub's numeric installation id"))
		return 0, false
	}
	return id, true
}

func (s *Server) handleGitHubAppUnbind(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	id, ok := installationIDFrom(w, r)
	if !ok {
		return
	}
	if err := t.UnbindGitHubAppInstallation(r.Context(), id); err != nil {
		writeIdentityError(w, s, r, "unbind github app installation", err)
		return
	}
	s.logger.Info("github app installation disconnected", "team", string(p.Team), "installation_id", id, "by", p.AccountID)
	w.WriteHeader(http.StatusNoContent)
}

// safety: the operator's route is how a binding moves between teams: an
// owner of the new team connects once the old binding is gone.
func (s *Server) handleOperatorGitHubAppUnbind(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	id, ok := installationIDFrom(w, r)
	if !ok {
		return
	}
	team, err := s.store.AsOperator().UnbindGitHubAppInstallation(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errors.New("no team holds that installation"))
		return
	}
	if err != nil {
		s.writeInternalError(w, r, "operator unbind github app installation", err)
		return
	}
	s.logger.Info("github app installation disconnected by the operator", "team", string(team), "installation_id", id)
	w.WriteHeader(http.StatusNoContent)
}

type githubAppRepositoryJSON struct {
	RepositoryID int64  `json:"repository_id"`
	FullName     string `json:"full_name"`
	Private      bool   `json:"private"`
}

func (s *Server) handleGitHubAppRepositories(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	id, ok := installationIDFrom(w, r)
	if !ok {
		return
	}
	if _, err := t.GitHubAppInstallation(r.Context(), id); err != nil {
		writeIdentityError(w, s, r, "github app installation", err)
		return
	}
	repos, err := s.githubApp.client.InstallationRepositories(r.Context(), id)
	if err != nil {
		s.logger.Warn("github app repositories", "installation_id", id, "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not list the installation's repositories"))
		return
	}
	out := []githubAppRepositoryJSON{}
	for _, repo := range repos {
		out = append(out, githubAppRepositoryJSON{RepositoryID: repo.ID, FullName: repo.FullName, Private: repo.Private})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": out})
}

type githubAppTriggerReq struct {
	Repository  string `json:"repository"`
	Pipeline    string `json:"pipeline"`
	Push        bool   `json:"push"`
	PullRequest bool   `json:"pull_request"`
}

type githubAppTriggerJSON struct {
	Repository     string `json:"repository"`
	RepositoryID   int64  `json:"repository_id"`
	InstallationID int64  `json:"installation_id"`
	Pipeline       string `json:"pipeline"`
	Push           bool   `json:"push"`
	PullRequest    bool   `json:"pull_request"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      int64  `json:"created_at"`
}

func githubAppTriggerOut(tr store.GitHubAppTrigger) githubAppTriggerJSON {
	return githubAppTriggerJSON{
		Repository: tr.Repository, RepositoryID: tr.RepositoryID, InstallationID: tr.InstallationID,
		Pipeline: tr.Pipeline, Push: tr.Push, PullRequest: tr.PullRequest,
		CreatedBy: tr.CreatedBy, CreatedAt: tr.CreatedAt.Unix(),
	}
}

func (s *Server) handleListGitHubAppTriggers(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	_, t, ok := s.teamMember(w, r, store.RoleReader)
	if !ok {
		return
	}
	list, err := t.GitHubAppTriggers(r.Context())
	if err != nil {
		s.writeInternalError(w, r, "list github app triggers", err)
		return
	}
	out := []githubAppTriggerJSON{}
	for _, tr := range list {
		out = append(out, githubAppTriggerOut(tr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggers": out})
}

// safety: the repository is resolved through GitHub to an installation this
// team holds, so a subscription can name only a repository the team proved
// it controls.
func (s *Server) handlePutGitHubAppTrigger(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	var req githubAppTriggerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pipeline, slug, err := validGitHubBinding(req.Pipeline, req.Repository)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !req.Push && !req.PullRequest {
		writeError(w, http.StatusBadRequest, errors.New("subscribe to push, pull_request or both"))
		return
	}
	repo, _ := store.ParseGitHubRepo(slug)
	inst, found, err := s.teamInstallationFor(r.Context(), t, repo)
	if err != nil {
		s.logger.Warn("github app trigger lookup", "repository", slug, "err", err.Error())
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not be reached to find the repository's installation"))
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("no installation this team holds covers "+slug))
		return
	}
	repos, err := s.githubApp.client.InstallationRepositories(r.Context(), inst.InstallationID)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not list the installation's repositories"))
		return
	}
	var match *githubapp.Repository
	for i := range repos {
		if strings.EqualFold(repos[i].FullName, slug) {
			match = &repos[i]
		}
	}
	if match == nil {
		writeError(w, http.StatusNotFound, errors.New("no installation this team holds covers "+slug))
		return
	}
	saved, err := t.PutGitHubAppTrigger(r.Context(), store.GitHubAppTrigger{
		RepositoryID: match.ID, Repository: match.FullName, InstallationID: inst.InstallationID,
		Pipeline: pipeline, Push: req.Push, PullRequest: req.PullRequest,
		CreatedBy: p.AccountID,
	}, time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "put github app trigger", err)
		return
	}
	s.logger.Info("github app trigger written", "team", string(p.Team), "repository", saved.Repository,
		"pipeline", saved.Pipeline, "push", saved.Push, "pull_request", saved.PullRequest,
		"by", p.AccountID)
	writeJSON(w, http.StatusOK, githubAppTriggerOut(saved))
}

func (s *Server) handleDeleteGitHubAppTrigger(w http.ResponseWriter, r *http.Request) {
	if !s.githubAppEnabled(w) {
		return
	}
	p, t, ok := s.teamMember(w, r, store.RoleOwner)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("repository_id"), 10, 64)
	pipeline := r.URL.Query().Get("pipeline")
	if err != nil || id <= 0 || pipeline == "" {
		writeError(w, http.StatusBadRequest, errors.New("repository_id and pipeline are required"))
		return
	}
	if err := t.DeleteGitHubAppTrigger(r.Context(), id, pipeline); err != nil {
		writeIdentityError(w, s, r, "delete github app trigger", err)
		return
	}
	s.logger.Info("github app trigger removed", "team", string(p.Team), "repository_id", id, "pipeline", pipeline, "by", p.AccountID)
	w.WriteHeader(http.StatusNoContent)
}

// safety: an installation another team holds, or a suspended one, reads as not found,
// so nothing mints a token from it.
func (s *Server) teamInstallationFor(ctx context.Context, t *store.Tenant, repo store.GitHubRepo) (store.GitHubAppInstallation, bool, error) {
	gh, covered, err := s.githubApp.coveringInstallation(ctx, repo, time.Now())
	if err != nil || !covered {
		return store.GitHubAppInstallation{}, false, err
	}
	in, err := t.GitHubAppInstallation(ctx, gh.ID)
	if errors.Is(err, store.ErrNotFound) {
		return store.GitHubAppInstallation{}, false, nil
	}
	if err != nil {
		return store.GitHubAppInstallation{}, false, err
	}
	if in.Suspended || gh.SuspendedAt != nil {
		return store.GitHubAppInstallation{}, false, nil
	}
	return in, true, nil
}

// safety: a run reports only while the installation it arrived through is still bound
// to the run's team.
func (s *Server) githubAppCommitStatus(ctx context.Context, trigger *store.Trigger, runStatus string) (githubCommitStatus, bool) {
	if s.githubApp == nil || trigger.Pipeline == "" || trigger.GithubOwner == "" || trigger.GithubRepo == "" {
		return githubCommitStatus{}, false
	}
	installation, err := strconv.ParseInt(trigger.TriggerEnv[envGitHubAppInstallation], 10, 64)
	if err != nil {
		return githubCommitStatus{}, false
	}
	in, err := s.store.AsOperator().GitHubAppInstallationTeam(ctx, installation)
	if err != nil || in.Team != store.NormalizeTeam(trigger.Team) || in.Suspended {
		return githubCommitStatus{}, false
	}
	sha := trigger.GitSHA
	if head := trigger.TriggerEnv[sparkwing.EnvPRHeadSHA]; trigger.TriggerEnv[sparkwing.EnvGitHubEventName] == sparkwing.EventPullRequest && head != "" {
		sha = head
	}
	if !githubCommit(strings.ToLower(sha)) {
		return githubCommitStatus{}, false
	}
	state, description := githubCommitState(runStatus)
	return githubCommitStatus{
		Installation: installation, Owner: trigger.GithubOwner, Repo: trigger.GithubRepo, SHA: sha,
		Pipeline: trigger.Pipeline, RunID: trigger.ID, State: state, Description: description,
	}, true
}

// coveringTTL bounds how stale GitHub's answer for a repository's
// installation may be when a run is created or a token minted.
const coveringTTL = time.Minute

// liveCoveringInstallation asks GitHub which installation covers repo,
// bypassing the cache, or answers false when none does.
func (a *githubAppState) liveCoveringInstallation(ctx context.Context, repo store.GitHubRepo) (githubapp.Installation, bool, error) {
	inst, err := a.client.RepositoryInstallation(ctx, repo.Owner, repo.Name)
	if errors.Is(err, githubapp.ErrNotInstalled) {
		return githubapp.Installation{}, false, nil
	}
	return inst, err == nil, err
}

// coveringInstallation answers the installation GitHub reports covers repo,
// or false when none does, from an answer at most [coveringTTL] old.
func (a *githubAppState) coveringInstallation(ctx context.Context, repo store.GitHubRepo, now time.Time) (githubapp.Installation, bool, error) {
	key := strings.ToLower(repo.Slug())
	a.mu.Lock()
	e, ok := a.covering[key]
	a.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.inst, !e.missing, nil
	}
	inst, err := a.client.RepositoryInstallation(ctx, repo.Owner, repo.Name)
	missing := errors.Is(err, githubapp.ErrNotInstalled)
	if err != nil && !missing {
		return githubapp.Installation{}, false, err
	}
	a.mu.Lock()
	for k, old := range a.covering {
		if !now.Before(old.until) {
			delete(a.covering, k)
		}
	}
	a.covering[key] = coveringEntry{inst: inst, missing: missing, until: now.Add(coveringTTL)}
	a.mu.Unlock()
	return inst, !missing, nil
}

func (a *githubAppState) forgetCovering() {
	a.mu.Lock()
	clear(a.covering)
	a.mu.Unlock()
}
