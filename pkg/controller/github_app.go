package controller

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
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
	checks   *githubCheckReporter
	cronMu   sync.Mutex

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
	if s.githubApp != nil {
		s.githubApp.checks.stop()
	}
	s.githubApp = &githubAppState{
		client: client, stateKey: client.StateKey(),
		checks:   newGitHubCheckReporter(client, s.store, s.dashboardURL),
		covering: map[string]coveringEntry{}, sources: map[string]*sourceTokenEntry{},
	}
	return s
}

func (s *Server) githubAppEnabled(w http.ResponseWriter) bool {
	if s.githubApp != nil {
		return true
	}
	writeError(w, http.StatusNotFound, errNoGitHubApp)
	return false
}

var errNoGitHubApp = errors.New("this controller has no GitHub App configured")

// safety: only an owner of the active team connects an installation, because the state this
// controller signs names that team.
func (s *Server) githubAppOwner(ctx context.Context, p *Principal) (*store.Tenant, error) {
	if s.githubApp == nil {
		return nil, refuse(http.StatusNotFound, "", errNoGitHubApp.Error())
	}
	if p == nil || p.AccountID == "" {
		return nil, refuse(http.StatusUnauthorized, "unauthenticated", "sign in to use this route")
	}
	if !store.Role(p.Role).AtLeast(store.RoleOwner) {
		return nil, refuse(http.StatusForbidden, "forbidden", "this needs the "+string(store.RoleOwner)+" role in the active team")
	}
	t, err := s.store.ForTeam(ctx, p.Team)
	if err != nil {
		return nil, fmt.Errorf("team handle: %w", err)
	}
	return t, nil
}

type githubConnectState struct {
	Team     string `json:"t"`
	Account  string `json:"a"`
	Expires  int64  `json:"e"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

func (a *githubAppState) signState(st githubConnectState) (string, error) {
	return signFlowState(a.stateKey, st)
}

var errConnectState = errors.New("the connect flow's state is not valid; start connecting again")

// safety: the state proves this controller started the flow for this account
// and team, and the verifier proves this browser is the one that started it;
// the caller records the nonce so a state finishes one flow only.
func (a *githubAppState) openState(raw, verifier string, p *Principal, now time.Time) (githubConnectState, error) {
	var st githubConnectState
	if !openFlowState(a.stateKey, raw, &st) {
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

type githubAppConnectGrant struct {
	InstallURL   string
	AuthorizeURL string
	State        string
	Verifier     string
}

// safety: only an owner connects, and only an account with a linked GitHub
// identity, because the identity is what the user GitHub reports is held to.
func (s *Server) githubAppConnectBegin(ctx context.Context, p *Principal, redirectURI string) (githubAppConnectGrant, error) {
	if _, err := s.githubAppOwner(ctx, p); err != nil {
		return githubAppConnectGrant{}, err
	}
	if !s.redirectAllowed(redirectURI) {
		return githubAppConnectGrant{}, refuse(http.StatusBadRequest, "", "redirect_uri is not on this controller's allowlist")
	}
	if _, err := s.githubIdentitySubject(ctx, p); err != nil {
		return githubAppConnectGrant{}, err
	}
	nonce, err := randomURLToken()
	if err != nil {
		return githubAppConnectGrant{}, err
	}
	verifier, err := randomURLToken()
	if err != nil {
		return githubAppConnectGrant{}, err
	}
	state, err := s.githubApp.signState(githubConnectState{
		Team: string(p.Team), Account: p.AccountID, Expires: time.Now().Add(githubAppStateTTL).Unix(),
		Nonce: nonce, Verifier: verifierDigest(verifier),
	})
	if err != nil {
		return githubAppConnectGrant{}, fmt.Errorf("github app connect: %w", err)
	}
	return githubAppConnectGrant{
		InstallURL:   s.githubApp.client.InstallURL(state),
		AuthorizeURL: s.githubApp.client.AuthorizeURL(state, verifier, redirectURI),
		State:        state,
		Verifier:     verifier,
	}, nil
}

func (s *Server) githubIdentitySubject(ctx context.Context, p *Principal) (string, error) {
	subject, err := s.store.AccountIdentity(ctx, p.AccountID, store.ProviderGitHub)
	if errors.Is(err, store.ErrNotFound) {
		return "", refuse(http.StatusForbidden, githubIdentityMissing, errNoGitHubIdentity.Error())
	}
	if err != nil {
		return "", fmt.Errorf("github app identity: %w", err)
	}
	return subject, nil
}

const githubIdentityMissing = "github_identity_missing"

var errNoGitHubIdentity = errors.New("link a GitHub sign-in to this account before connecting the GitHub App")

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

// safety: the refusal is the same whatever holds the installation, so a
// team owner cannot learn another team's GitHub bindings from it.
var errGitHubInstallationUnavailable = errors.New("this installation is unavailable to this team; contact the operator if you expected to connect it")

func (s *Server) githubAppAdministers(ctx context.Context, token string, user githubapp.User, inst githubapp.Installation) (bool, error) {
	switch inst.Account.Type {
	case "User":
		return inst.Account.ID == user.ID, nil
	case "Organization":
		membership, err := s.githubApp.client.UserOrgMembership(ctx, token, inst.Account.Login)
		return membership.Admin(), err
	default:
		return false, nil
	}
}

type githubAppAvailableInstallation struct {
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
	AccountType    string `json:"account_type"`
}

type githubAppAvailableResp struct {
	Authorization string                           `json:"authorization"`
	Installations []githubAppAvailableInstallation `json:"installations"`
}

type githubAppSelectionProof struct {
	Nonce           string  `json:"nonce"`
	UserID          int64   `json:"user_id"`
	Token           string  `json:"token"`
	InstallationIDs []int64 `json:"installation_ids"`
}

func (a *githubAppState) selectionCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append(append([]byte(nil), a.stateKey...), []byte("selection proof")...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (a *githubAppState) sealSelection(proof githubAppSelectionProof, state string) (string, error) {
	aead, err := a.selectionCipher()
	if err != nil {
		return "", err
	}
	plain, err := json.Marshal(proof)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(nonce, aead.Seal(nil, nonce, plain, []byte(state))...)), nil
}

func (a *githubAppState) openSelection(raw, state string) (githubAppSelectionProof, error) {
	aead, err := a.selectionCipher()
	if err != nil {
		return githubAppSelectionProof{}, err
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) < aead.NonceSize() {
		return githubAppSelectionProof{}, errConnectState
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(state))
	if err != nil {
		return githubAppSelectionProof{}, errConnectState
	}
	var proof githubAppSelectionProof
	if err := json.Unmarshal(plain, &proof); err != nil || proof.Nonce == "" || proof.Token == "" || proof.UserID <= 0 {
		return githubAppSelectionProof{}, errConnectState
	}
	return proof, nil
}

func (s *Server) githubAppAvailable(ctx context.Context, p *Principal, flow oauthFlowProof, redirectURI string) (githubAppAvailableResp, error) {
	if _, err := s.githubAppOwner(ctx, p); err != nil {
		return githubAppAvailableResp{}, err
	}
	if flow.Code == "" || !s.redirectAllowed(redirectURI) {
		return githubAppAvailableResp{}, refuse(http.StatusBadRequest, "", "code and allowed redirect_uri are required")
	}
	st, err := s.githubApp.openState(flow.State, flow.Verifier, p, time.Now())
	if err != nil {
		return githubAppAvailableResp{}, refuse(http.StatusForbidden, "", err.Error())
	}
	subject, err := s.githubIdentitySubject(ctx, p)
	if err != nil {
		return githubAppAvailableResp{}, err
	}
	out := githubAppAvailableResp{Installations: []githubAppAvailableInstallation{}}
	_, err = s.githubApp.client.ExchangeUserCode(ctx, flow.Code, flow.Verifier, redirectURI,
		func(ctx context.Context, token string, user githubapp.User) error {
			if strconv.FormatInt(user.ID, 10) != subject {
				return errGitHubUserMismatch
			}
			installations, err := s.githubApp.client.UserInstallations(ctx, token)
			if err != nil {
				return err
			}
			for _, inst := range installations {
				admin, err := s.githubAppAdministers(ctx, token, user, inst)
				if err != nil {
					return err
				}
				if !admin {
					continue
				}
				out.Installations = append(out.Installations, githubAppAvailableInstallation{
					InstallationID: inst.ID, AccountLogin: inst.Account.Login, AccountType: inst.Account.Type,
				})
			}
			ids := make([]int64, 0, len(out.Installations))
			for _, inst := range out.Installations {
				ids = append(ids, inst.InstallationID)
			}
			out.Authorization, err = s.githubApp.sealSelection(githubAppSelectionProof{
				Nonce: st.Nonce, UserID: user.ID, Token: token, InstallationIDs: ids,
			}, flow.State)
			return err
		})
	if err != nil {
		return githubAppAvailableResp{}, s.githubAppConnectRefusal(p, 0, err)
	}
	return out, nil
}

func (s *Server) githubAppSelect(ctx context.Context, p *Principal, flow oauthFlowProof, authorization string, installationID int64) (store.GitHubAppInstallation, error) {
	t, err := s.githubAppOwner(ctx, p)
	if err != nil {
		return store.GitHubAppInstallation{}, err
	}
	if installationID <= 0 {
		return store.GitHubAppInstallation{}, refuse(http.StatusBadRequest, "", "installation_id is required")
	}
	now := time.Now()
	st, err := s.githubApp.openState(flow.State, flow.Verifier, p, now)
	if err != nil {
		return store.GitHubAppInstallation{}, refuse(http.StatusForbidden, "", err.Error())
	}
	proof, err := s.githubApp.openSelection(authorization, flow.State)
	if err != nil || proof.Nonce != st.Nonce {
		return store.GitHubAppInstallation{}, refuse(http.StatusForbidden, "", errConnectState.Error())
	}
	subject, err := s.githubIdentitySubject(ctx, p)
	if err != nil {
		return store.GitHubAppInstallation{}, err
	}
	if strconv.FormatInt(proof.UserID, 10) != subject {
		return store.GitHubAppInstallation{}, refuse(http.StatusForbidden, "", errGitHubUserMismatch.Error())
	}
	fresh, err := s.store.ConsumeGitHubAppConnectState(ctx, st.Nonce, time.Unix(st.Expires, 0), now)
	if err != nil {
		return store.GitHubAppInstallation{}, fmt.Errorf("github app connect state: %w", err)
	}
	if !fresh {
		return store.GitHubAppInstallation{}, refuse(http.StatusForbidden, "", "the connect flow was already used; start connecting again")
	}
	if !slices.Contains(proof.InstallationIDs, installationID) {
		return store.GitHubAppInstallation{}, refuse(http.StatusNotFound, "", "installation not found")
	}
	inst, err := s.githubApp.client.Installation(ctx, installationID)
	if err != nil {
		return store.GitHubAppInstallation{}, s.githubAppConnectRefusal(p, installationID, err)
	}
	admin, err := s.githubAppAdministers(ctx, proof.Token, githubapp.User{ID: proof.UserID}, inst)
	if err != nil {
		return store.GitHubAppInstallation{}, s.githubAppConnectRefusal(p, installationID, err)
	}
	if !admin {
		return store.GitHubAppInstallation{}, refuse(http.StatusForbidden, "", errGitHubInstallationAccess.Error())
	}
	return s.bindGitHubAppInstallation(ctx, t, p, inst, proof.UserID, now)
}

func (s *Server) bindGitHubAppInstallation(ctx context.Context, t *store.Tenant, p *Principal, inst githubapp.Installation, githubUser int64, now time.Time) (store.GitHubAppInstallation, error) {
	bound, err := t.BindGitHubAppInstallation(ctx, store.GitHubAppInstallation{
		InstallationID: inst.ID, AccountID: inst.Account.ID, AccountLogin: inst.Account.Login,
		AccountType: inst.Account.Type, Suspended: inst.SuspendedAt != nil,
		ConnectedBy: p.AccountID, GitHubUserID: githubUser,
	}, now)
	if errors.Is(err, store.ErrInstallationBoundElsewhere) {
		return store.GitHubAppInstallation{}, refuse(http.StatusConflict, "", errGitHubInstallationUnavailable.Error())
	}
	if err != nil {
		return store.GitHubAppInstallation{}, identityRefusal("github app bind", err)
	}
	s.logger.Info("github app installation connected", "team", string(p.Team), "installation_id", inst.ID,
		"account", inst.Account.Login, "account_type", inst.Account.Type, "by", p.AccountID, "github_user", githubUser)
	return bound, nil
}

// safety: seeing an installation is not enough to bind it, because an
// organization member with read access to one repository sees the
// organization's installation; binding needs the account's own user or an
// organization admin, which is who GitHub lets install the App.
func (s *Server) githubAppConnectFinish(ctx context.Context, p *Principal, flow oauthFlowProof, installationID int64, redirectURI string) (store.GitHubAppInstallation, error) {
	t, err := s.githubAppOwner(ctx, p)
	if err != nil {
		return store.GitHubAppInstallation{}, err
	}
	if flow.Code == "" || installationID <= 0 {
		return store.GitHubAppInstallation{}, refuse(http.StatusBadRequest, "", "code and installation_id are required")
	}
	if !s.redirectAllowed(redirectURI) {
		return store.GitHubAppInstallation{}, refuse(http.StatusBadRequest, "", "redirect_uri is not on this controller's allowlist")
	}
	now := time.Now()
	st, err := s.githubApp.openState(flow.State, flow.Verifier, p, now)
	if err == nil {
		// safety: the used state is recorded in the store, so no replica and no
		// restart finishes the same flow twice.
		fresh, cerr := s.store.ConsumeGitHubAppConnectState(ctx, st.Nonce, time.Unix(st.Expires, 0), now)
		if cerr != nil {
			return store.GitHubAppInstallation{}, fmt.Errorf("github app connect state: %w", cerr)
		}
		if !fresh {
			err = errors.New("the connect flow was already used; start connecting again")
		}
	}
	if err != nil {
		s.logger.Info("github app connect refused", "team", string(p.Team), "account", p.AccountID, "reason", err.Error())
		return store.GitHubAppInstallation{}, refuse(http.StatusForbidden, "", err.Error())
	}
	subject, err := s.githubIdentitySubject(ctx, p)
	if err != nil {
		return store.GitHubAppInstallation{}, err
	}
	var inst githubapp.Installation
	check := func(ctx context.Context, userToken string, u githubapp.User) error {
		if strconv.FormatInt(u.ID, 10) != subject {
			return errGitHubUserMismatch
		}
		got, err := s.githubApp.client.Installation(ctx, installationID)
		if err != nil {
			return err
		}
		admin, err := s.githubAppAdministers(ctx, userToken, u, got)
		if err != nil {
			return err
		}
		if !admin {
			return errGitHubInstallationAccess
		}
		inst = got
		return nil
	}
	user, err := s.githubApp.client.ExchangeUserCode(ctx, flow.Code, flow.Verifier, redirectURI, check)
	if err != nil {
		return store.GitHubAppInstallation{}, s.githubAppConnectRefusal(p, installationID, err)
	}
	return s.bindGitHubAppInstallation(ctx, t, p, inst, user.ID, time.Now())
}

func (s *Server) githubAppConnectRefusal(p *Principal, installation int64, err error) error {
	s.logger.Info("github app connect refused", "team", string(p.Team), "account", p.AccountID,
		"installation_id", installation, "reason", err.Error())
	switch {
	case errors.Is(err, githubapp.ErrNotInstalled):
		return refuse(http.StatusNotFound, "", "GitHub reports no installation of the App by that id")
	case errors.Is(err, githubapp.ErrRejected):
		return refuse(http.StatusForbidden, "", "GitHub did not confirm the authorization; start connecting again")
	case errors.Is(err, errGitHubInstallationAccess), errors.Is(err, errGitHubUserMismatch):
		return refuse(http.StatusForbidden, "", err.Error())
	default:
		s.logger.Warn("github app connect failed", "err", err.Error())
		return refuse(http.StatusBadGateway, "", "GitHub could not be reached to finish connecting")
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
	// safety: a list cut at GitHub's listing bound only displays; nothing is
	// refused or withdrawn for a repository missing from it.
	if err != nil && !errors.Is(err, githubapp.ErrTooManyRepositories) {
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
	Repository                string   `json:"repository"`
	Pipeline                  string   `json:"pipeline"`
	Push                      bool     `json:"push"`
	Tags                      []string `json:"tags"`
	PullRequest               bool     `json:"pull_request"`
	Branches                  []string `json:"branches"`
	BaseBranches              []string `json:"base_branches"`
	PullRequestClosed         bool     `json:"pull_request_closed"`
	PullRequestLabeled        bool     `json:"pull_request_labeled"`
	PullRequestLabels         []string `json:"pull_request_labels"`
	PullRequestReadyForReview bool     `json:"pull_request_ready_for_review"`
	ReleasePublished          bool     `json:"release_published"`
	ReleasePrereleased        bool     `json:"release_prereleased"`
	BranchCreate              bool     `json:"branch_create"`
	BranchDelete              bool     `json:"branch_delete"`
}

type githubAppTriggerJSON struct {
	Repository                string   `json:"repository"`
	RepositoryID              int64    `json:"repository_id"`
	InstallationID            int64    `json:"installation_id"`
	Pipeline                  string   `json:"pipeline"`
	Push                      bool     `json:"push"`
	Tags                      []string `json:"tags"`
	PullRequest               bool     `json:"pull_request"`
	Branches                  []string `json:"branches"`
	BaseBranches              []string `json:"base_branches"`
	PullRequestClosed         bool     `json:"pull_request_closed"`
	PullRequestLabeled        bool     `json:"pull_request_labeled"`
	PullRequestLabels         []string `json:"pull_request_labels"`
	PullRequestReadyForReview bool     `json:"pull_request_ready_for_review"`
	ReleasePublished          bool     `json:"release_published"`
	ReleasePrereleased        bool     `json:"release_prereleased"`
	BranchCreate              bool     `json:"branch_create"`
	BranchDelete              bool     `json:"branch_delete"`
	CreatedBy                 string   `json:"created_by"`
	CreatedAt                 int64    `json:"created_at"`
}

func githubAppTriggerOut(tr store.GitHubAppTrigger) githubAppTriggerJSON {
	if tr.Tags == nil {
		tr.Tags = []string{}
	}
	return githubAppTriggerJSON{
		Repository: tr.Repository, RepositoryID: tr.RepositoryID, InstallationID: tr.InstallationID,
		Pipeline: tr.Pipeline, Push: tr.Push, Tags: tr.Tags, PullRequest: tr.PullRequest,
		Branches: tr.Branches, BaseBranches: tr.BaseBranches,
		PullRequestClosed: tr.PullRequestClosed, PullRequestLabeled: tr.PullRequestLabeled,
		PullRequestLabels: tr.PullRequestLabels, PullRequestReadyForReview: tr.PullRequestReadyForReview,
		ReleasePublished: tr.ReleasePublished, ReleasePrereleased: tr.ReleasePrereleased,
		BranchCreate: tr.BranchCreate, BranchDelete: tr.BranchDelete,
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
	pipeline, slug, err := validGitHubSubscription(req.Pipeline, req.Repository)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := store.ValidateGitHubTagPatterns(req.Tags); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !req.Push && len(req.Tags) == 0 && !req.PullRequest && !req.PullRequestClosed && !req.PullRequestLabeled && !req.PullRequestReadyForReview &&
		!req.ReleasePublished && !req.ReleasePrereleased && !req.BranchCreate && !req.BranchDelete {
		writeError(w, http.StatusBadRequest, errors.New("subscribe to at least one event"))
		return
	}
	if req.PullRequestLabeled && len(req.PullRequestLabels) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("pull_request_labeled needs pull_request_labels"))
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
	truncated := errors.Is(err, githubapp.ErrTooManyRepositories)
	if err != nil && !truncated {
		writeError(w, http.StatusBadGateway, errors.New("GitHub could not list the installation's repositories"))
		return
	}
	var match *githubapp.Repository
	for i := range repos {
		if strings.EqualFold(repos[i].FullName, slug) {
			match = &repos[i]
		}
	}
	if match == nil && truncated {
		writeError(w, http.StatusBadGateway, errors.New("the installation covers more repositories than GitHub lists, so "+slug+" cannot be confirmed"))
		return
	}
	if match == nil {
		writeError(w, http.StatusNotFound, errors.New("no installation this team holds covers "+slug))
		return
	}
	saved, err := t.PutGitHubAppTrigger(r.Context(), store.GitHubAppTrigger{
		RepositoryID: match.ID, Repository: match.FullName, InstallationID: inst.InstallationID,
		Pipeline: pipeline, Push: req.Push, Tags: req.Tags, PullRequest: req.PullRequest,
		Branches: req.Branches, BaseBranches: req.BaseBranches,
		PullRequestClosed: req.PullRequestClosed, PullRequestLabeled: req.PullRequestLabeled,
		PullRequestLabels: req.PullRequestLabels, PullRequestReadyForReview: req.PullRequestReadyForReview,
		ReleasePublished: req.ReleasePublished, ReleasePrereleased: req.ReleasePrereleased,
		BranchCreate: req.BranchCreate, BranchDelete: req.BranchDelete,
		CreatedBy: p.AccountID,
	}, time.Now())
	if err != nil {
		writeIdentityError(w, s, r, "put github app trigger", err)
		return
	}
	s.logger.Info("github app trigger written", "team", string(p.Team), "repository", saved.Repository,
		"pipeline", saved.Pipeline, "push", saved.Push, "tags", saved.Tags, "pull_request", saved.PullRequest,
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
func (s *Server) githubAppCheckUpdate(ctx context.Context, trigger *store.Trigger, runStatus string) (githubCheckUpdate, bool) {
	if s.githubApp == nil || trigger.Pipeline == "" || trigger.GithubOwner == "" || trigger.GithubRepo == "" {
		return githubCheckUpdate{}, false
	}
	installation, err := strconv.ParseInt(trigger.TriggerEnv[envGitHubAppInstallation], 10, 64)
	if err != nil {
		return githubCheckUpdate{}, false
	}
	in, err := s.store.AsOperator().GitHubAppInstallationTeam(ctx, installation)
	if err != nil || in.Team != store.NormalizeTeam(trigger.Team) || in.Suspended {
		return githubCheckUpdate{}, false
	}
	sha := trigger.GitSHA
	if head := trigger.TriggerEnv[sparkwing.EnvPRHeadSHA]; trigger.TriggerEnv[sparkwing.EnvGitHubEventName] == sparkwing.EventPullRequest && head != "" {
		sha = head
	}
	if !githubCommit(strings.ToLower(sha)) {
		return githubCheckUpdate{}, false
	}
	return githubCheckUpdate{
		installation: installation, owner: trigger.GithubOwner, repo: trigger.GithubRepo, sha: sha,
		pipeline: trigger.Pipeline, runID: trigger.ID, runStatus: runStatus, team: in.Team,
	}, true
}

// perf: Reuse a covering installation briefly rather than call GitHub for every run.
const coveringTTL = time.Minute

// safety: Bypass the cache when verifying which installation can issue a token for this repository.
func (a *githubAppState) liveCoveringInstallation(ctx context.Context, repo store.GitHubRepo) (githubapp.Installation, bool, error) {
	inst, err := a.client.RepositoryInstallation(ctx, repo.Owner, repo.Name)
	if errors.Is(err, githubapp.ErrNotInstalled) {
		return githubapp.Installation{}, false, nil
	}
	return inst, err == nil, err
}

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

func validGitHubSubscription(pipeline, repo string) (string, string, error) {
	pipeline = strings.TrimSpace(pipeline)
	if pipeline == "" {
		return "", "", errors.New("pipeline is required")
	}
	if pipeline != url.PathEscape(pipeline) {
		return "", "", fmt.Errorf("pipeline %q is not a single URL path segment", pipeline)
	}
	slug, ok := normalizeGitHubRepo(strings.TrimSpace(repo))
	if !ok {
		return "", "", fmt.Errorf("repo %q is not an ascii owner/name slug", repo)
	}
	return pipeline, slug, nil
}

func normalizeGitHubRepo(slug string) (string, bool) {
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || !asciiSlugPart(owner) || !asciiSlugPart(name) {
		return "", false
	}
	return strings.ToLower(slug), true
}

func asciiSlugPart(part string) bool {
	if part == "" || len(part) > 100 {
		return false
	}
	for i := range len(part) {
		c := part[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}
