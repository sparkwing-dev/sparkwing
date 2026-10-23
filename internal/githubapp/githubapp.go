// Package githubapp is the controller's client for its GitHub App: it signs
// the App's own JWT, reads installations, mints installation tokens
// restricted to one repository, redeems a user's authorization code, and
// verifies webhook signatures. The App's private key never leaves this
// package; callers receive only the short-lived tokens it mints.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/githubauth"

	"github.com/sparkwing-dev/sparkwing/internal/googleauth"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

// GitHub's production endpoints.
const (
	gitHubWebURL = "https://github.com"
	gitHubAPIURL = "https://api.github.com"
)

const (
	apiVersion = "2022-11-28"
	// safety: GitHub stopped answering fails the request instead of holding it.
	requestTimeout = 10 * time.Second
	maxBody        = 1 << 20
	// safety: GitHub refuses an App JWT that lives longer than ten minutes, and
	// backdating iat absorbs clock drift between this host and GitHub.
	jwtLifetime = 9 * time.Minute
	jwtBackdate = time.Minute
)

// Errors a caller distinguishes. ErrRejected covers every answer that means
// the credential presented does not prove what the caller asked GitHub to
// confirm; ErrNotInstalled means GitHub reports no installation of the App
// for what was asked about.
var (
	ErrRejected     = errors.New("githubapp: github rejected the credential")
	ErrNotInstalled = errors.New("githubapp: the app is not installed there")
	// ErrPermissionMissing means the installation has not granted a
	// permission the request needs, as when its owner has not yet accepted
	// one the App added.
	ErrPermissionMissing = errors.New("githubapp: the installation has not granted the permission")
)

type apiError struct {
	op     string
	status int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("githubapp: %s answered %d", e.op, e.status)
}

// Temporary reports whether the same request may succeed later: GitHub
// answered 429 or a 5xx.
func (e *apiError) Temporary() bool {
	return e.status == http.StatusTooManyRequests || e.status >= 500
}

// Config names the App and where GitHub answers.
type Config struct {
	AppID         int64
	Slug          string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	// PreviousWebhookSecret also verifies deliveries while a rotation settles; empty disables it.
	PreviousWebhookSecret string
	PrivateKey            *rsa.PrivateKey
	// WebURL is github.com, where browsers install the App and authorize it.
	WebURL string
	// APIURL is api.github.com.
	APIURL string
	HTTP   *http.Client
}

// Validate reports what is missing from c.
func (c Config) Validate() error {
	var missing []string
	if c.AppID <= 0 {
		missing = append(missing, "app id")
	}
	if c.Slug == "" {
		missing = append(missing, "slug")
	}
	if c.PrivateKey == nil {
		missing = append(missing, "private key")
	}
	if c.WebhookSecret == "" {
		missing = append(missing, "webhook secret")
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		missing = append(missing, "client id and secret")
	}
	if len(missing) > 0 {
		return fmt.Errorf("githubapp: the app needs %s", strings.Join(missing, ", "))
	}
	return nil
}

// ParsePrivateKey reads the PEM GitHub issues for an App (PKCS#1) or a PKCS#8
// RSA key.
func ParsePrivateKey(pemText []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemText)
	if block == nil {
		return nil, errors.New("githubapp: the private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("githubapp: the private key is not RSA")
	}
	return key, nil
}

// Client talks to GitHub as the App.
type Client struct{ cfg Config }

// New returns a client for cfg, filling GitHub's production endpoints for
// any left empty.
func New(cfg Config) *Client {
	if cfg.WebURL == "" {
		cfg.WebURL = gitHubWebURL
	}
	if cfg.APIURL == "" {
		cfg.APIURL = gitHubAPIURL
	}
	cfg.WebURL = strings.TrimRight(cfg.WebURL, "/")
	cfg.APIURL = strings.TrimRight(cfg.APIURL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: requestTimeout}
	}
	return &Client{cfg: cfg}
}

// Slug is the App's public name.
func (c *Client) Slug() string { return c.cfg.Slug }

// AppID is the App's numeric id, which GitHub names in the check runs and
// check suites the App owns.
func (c *Client) AppID() int64 { return c.cfg.AppID }

// StateKey derives a key for signing connect-flow state from the App's
// private key, so every controller replica holding the key agrees on it and
// nothing else can produce it.
func (c *Client) StateKey() []byte {
	mac := hmac.New(sha256.New, []byte("sparkwing github app connect state v1"))
	mac.Write(x509.MarshalPKCS1PrivateKey(c.cfg.PrivateKey))
	return mac.Sum(nil)
}

// InstallURL is where a browser installs the App; GitHub hands state back on
// the setup redirect.
func (c *Client) InstallURL(state string) string {
	return c.cfg.WebURL + "/apps/" + url.PathEscape(c.cfg.Slug) + "/installations/new?" +
		url.Values{"state": {state}}.Encode()
}

// AuthorizeURL is where a browser authorizes the App to act for its user.
// The PKCE challenge binds the code GitHub returns to verifier.
func (c *Client) AuthorizeURL(state, verifier, redirectURI string) string {
	q := url.Values{
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {googleauth.CodeChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	return c.cfg.WebURL + "/login/oauth/authorize?" + q.Encode()
}

// WebhookMAC hashes a streamed delivery body under every accepted webhook secret.
type WebhookMAC struct{ macs []hash.Hash }

// WebhookMAC starts a delivery hash under the current and any previous secret.
func (c *Client) WebhookMAC() *WebhookMAC {
	m := &WebhookMAC{macs: []hash.Hash{hmac.New(sha256.New, []byte(c.cfg.WebhookSecret))}}
	if c.cfg.PreviousWebhookSecret != "" {
		m.macs = append(m.macs, hmac.New(sha256.New, []byte(c.cfg.PreviousWebhookSecret)))
	}
	return m
}

func (m *WebhookMAC) Write(p []byte) (int, error) {
	for _, mac := range m.macs {
		mac.Write(p)
	}
	return len(p), nil
}

// Verify reports whether signature is the HMAC of the bytes written under any accepted secret.
func (m *WebhookMAC) Verify(signature string) bool {
	hexSum, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	want, err := hex.DecodeString(hexSum)
	if err != nil {
		return false
	}
	verified := false
	for _, mac := range m.macs {
		verified = hmac.Equal(want, mac.Sum(nil)) || verified
	}
	return verified
}

// User is a GitHub account as a user token reports it.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Account is the owner of an installation.
type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	// Type is "User" or "Organization".
	Type string `json:"type"`
}

// Installation is GitHub's record of one installation of the App.
type Installation struct {
	ID                  int64      `json:"id"`
	AppID               int64      `json:"app_id"`
	Account             Account    `json:"account"`
	RepositorySelection string     `json:"repository_selection"`
	SuspendedAt         *time.Time `json:"suspended_at"`
}

// Repository is a repository an installation covers.
type Repository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	// DefaultBranch is the branch GitHub serves as the repository's head.
	DefaultBranch string  `json:"default_branch"`
	Owner         Account `json:"owner"`
}

// Token is an installation access token.
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ExchangeUserCode redeems an authorization code and returns the GitHub user
// it belongs to. The user's token is used for the membership check the
// caller names in check and then dropped, so it is never stored.
func (c *Client) ExchangeUserCode(ctx context.Context, code, verifier, redirectURI string, check func(ctx context.Context, userToken string, u User) error) (User, error) {
	accessToken, err := githubauth.New(githubauth.Config{
		ClientID: c.cfg.ClientID, ClientSecret: c.cfg.ClientSecret,
		TokenURL: c.cfg.WebURL + "/login/oauth/access_token", HTTP: c.cfg.HTTP,
	}).RedeemCode(ctx, code, verifier, redirectURI)
	if errors.Is(err, githubauth.ErrRejected) {
		return User{}, fmt.Errorf("%w: the authorization code did not redeem: %w", ErrRejected, err)
	}
	if err != nil {
		return User{}, err
	}
	var u User
	if err := c.getJSON(ctx, "/user", "Bearer "+accessToken, &u); err != nil {
		return User{}, err
	}
	if u.ID <= 0 {
		return User{}, fmt.Errorf("%w: github returned no user id", ErrRejected)
	}
	if check != nil {
		if err := check(ctx, accessToken, u); err != nil {
			return User{}, err
		}
	}
	return u, nil
}

// OrgMembership is a user's standing in an organization.
type OrgMembership struct {
	State string `json:"state"`
	Role  string `json:"role"`
}

// Admin reports an active membership with the admin role.
func (m OrgMembership) Admin() bool { return m.State == "active" && m.Role == "admin" }

// UserOrgMembership reads the user's membership in org with the user's own
// token. A user who is not a member answers the zero membership, which is
// not [OrgMembership.Admin].
func (c *Client) UserOrgMembership(ctx context.Context, userToken, org string) (OrgMembership, error) {
	var m OrgMembership
	err := c.getJSON(ctx, "/user/memberships/orgs/"+url.PathEscape(org), "Bearer "+userToken, &m)
	if errors.Is(err, errNotFound) {
		return OrgMembership{}, nil
	}
	return m, err
}

// UserInstallations lists installations visible to the authorized user. Visibility
// alone does not prove that the user administers the installation's account.
func (c *Client) UserInstallations(ctx context.Context, userToken string) ([]Installation, error) {
	var all []Installation
	for page := 1; page <= 100; page++ {
		var answer struct {
			Installations []Installation `json:"installations"`
		}
		path := "/user/installations?per_page=100&page=" + strconv.Itoa(page)
		if err := c.getJSON(ctx, path, "Bearer "+userToken, &answer); err != nil {
			return nil, err
		}
		for _, inst := range answer.Installations {
			if inst.AppID == c.cfg.AppID {
				all = append(all, inst)
			}
		}
		if len(answer.Installations) < 100 {
			return all, nil
		}
	}
	return nil, errors.New("githubapp: too many user installations")
}

// Installation reads installation id with the App's own credential.
func (c *Client) Installation(ctx context.Context, id int64) (Installation, error) {
	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return Installation{}, err
	}
	var inst Installation
	err = c.getJSON(ctx, "/app/installations/"+strconv.FormatInt(id, 10), "Bearer "+jwt, &inst)
	if errors.Is(err, errNotFound) {
		return Installation{}, ErrNotInstalled
	}
	if err == nil && inst.ID != id {
		return Installation{}, fmt.Errorf("githubapp: asked for installation %d and got %d", id, inst.ID)
	}
	return inst, err
}

// RepositoryInstallation reads the installation of the App that covers
// owner/name, or ErrNotInstalled.
func (c *Client) RepositoryInstallation(ctx context.Context, owner, name string) (Installation, error) {
	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return Installation{}, err
	}
	var inst Installation
	err = c.getJSON(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/installation", "Bearer "+jwt, &inst)
	if errors.Is(err, errNotFound) {
		return Installation{}, ErrNotInstalled
	}
	return inst, err
}

// installationToken mints a token for installation restricted to the named
// repositories (each without its owner) and to permissions, such as
// {"contents": "read"}. GitHub refuses a repository the installation does not
// cover, which surfaces as ErrNotInstalled.
func (c *Client) installationToken(ctx context.Context, installation int64, repositories []string, permissions map[string]string) (Token, error) {
	// safety: GitHub reads an empty list as every repository the installation
	// covers, so a token is never minted without naming what it reads.
	if len(repositories) == 0 || slices.Contains(repositories, "") || len(permissions) == 0 {
		return Token{}, errors.New("githubapp: an installation token needs its repositories and permissions")
	}
	return c.mintToken(ctx, installation, map[string]any{"repositories": repositories, "permissions": permissions}, permissions)
}

// InstallationTokenByIDs mints a token restricted to repository IDs and
// permissions, so a rename or a reused name cannot redirect it.
func (c *Client) InstallationTokenByIDs(ctx context.Context, installation int64, ids []int64, permissions map[string]string) (Token, error) {
	if len(ids) == 0 || slices.Contains(ids, 0) || len(permissions) == 0 {
		return Token{}, errors.New("githubapp: an installation token needs its repository ids and permissions")
	}
	return c.mintToken(ctx, installation, map[string]any{"repository_ids": ids, "permissions": permissions}, permissions)
}

func (c *Client) mintToken(ctx context.Context, installation int64, request map[string]any, permissions map[string]string) (Token, error) {
	jwt, err := c.appJWT(time.Now())
	if err != nil {
		return Token{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.APIURL+"/app/installations/"+strconv.FormatInt(installation, 10)+"/access_tokens", bytes.NewReader(body))
	if err != nil {
		return Token{}, err
	}
	c.apiHeaders(req, "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	status, raw, err := c.send(req)
	if err != nil {
		return Token{}, err
	}
	var tok Token
	if status < 300 && json.Unmarshal(raw, &tok) != nil {
		return Token{}, fmt.Errorf("githubapp: unreadable answer from %s", req.URL.Path)
	}
	switch {
	case status == http.StatusUnprocessableEntity && permissionsNotGranted(raw):
		return Token{}, fmt.Errorf("%w: %v", ErrPermissionMissing, permissions)
	case status == http.StatusNotFound || status == http.StatusUnprocessableEntity:
		return Token{}, ErrNotInstalled
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Token{}, fmt.Errorf("%w: installation token for %d answered %d", ErrRejected, installation, status)
	case status != http.StatusCreated && status != http.StatusOK:
		return Token{}, &apiError{op: "installation token", status: status}
	case tok.Token == "":
		return Token{}, errors.New("githubapp: github returned no installation token")
	}
	return tok, nil
}

// InstallationRepositories lists the repositories installation covers with a
// token that can read nothing but metadata. It reads every page.
func (c *Client) InstallationRepositories(ctx context.Context, installation int64) ([]Repository, error) {
	permissions := map[string]string{"metadata": "read"}
	tok, err := c.mintToken(ctx, installation, map[string]any{"permissions": permissions}, permissions)
	if err != nil {
		return nil, err
	}
	var out []Repository
	for page := 1; ; page++ {
		var resp struct {
			Repositories []Repository `json:"repositories"`
		}
		path := "/installation/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := c.getJSON(ctx, path, "token "+tok.Token, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Repositories...)
		if len(resp.Repositories) < 100 {
			return out, nil
		}
	}
}

// RepositoryByID resolves the current name under an already restricted token.
func (c *Client) RepositoryByID(ctx context.Context, token string, id int64) (Repository, error) {
	if id <= 0 {
		return Repository{}, errors.New("githubapp: repository id must be positive")
	}
	var repo Repository
	err := c.getJSON(ctx, "/repositories/"+strconv.FormatInt(id, 10), "token "+token, &repo)
	return repo, err
}

// ErrRefNotFound means the repository has no requested ref.
var ErrRefNotFound = errors.New("githubapp: source ref is unavailable")

// ResolveCommit reads the commit selected by a qualified ref or full commit ID
// with a token restricted to that repository.
func (c *Client) ResolveCommit(ctx context.Context, installation int64, owner, repo, ref string) (string, error) {
	if ref != "HEAD" && !runspec.IsCommitID(ref) && !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
		return "", errors.New("githubapp: source ref must be qualified")
	}
	tok, err := c.installationToken(ctx, installation, []string{repo}, map[string]string{"contents": "read"})
	if err != nil {
		return "", err
	}
	return c.resolveCommit(ctx, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), "Bearer "+tok.Token, ref)
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (c *Client) resolveCommit(ctx context.Context, root, auth, ref string) (string, error) {
	if ref == "HEAD" {
		var repository struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := c.getJSON(ctx, root, auth, &repository); err != nil {
			return "", err
		}
		if repository.DefaultBranch == "" {
			return "", errors.New("githubapp: repository has no default branch")
		}
		ref = "refs/heads/" + repository.DefaultBranch
	}
	if runspec.IsCommitID(ref) {
		var commit struct {
			SHA string `json:"sha"`
		}
		err := c.getJSON(ctx, root+"/commits/"+url.PathEscape(ref), auth, &commit)
		if errors.Is(err, errNotFound) {
			return "", ErrRefNotFound
		}
		if err != nil {
			return "", err
		}
		if commit.SHA != ref {
			return "", errors.New("githubapp: commit identity changed")
		}
		return commit.SHA, nil
	}
	var reference struct {
		Ref    string    `json:"ref"`
		Object gitObject `json:"object"`
	}
	err := c.getJSON(ctx, root+"/git/ref/"+url.PathEscape(strings.TrimPrefix(ref, "refs/")), auth, &reference)
	if errors.Is(err, errNotFound) {
		return "", ErrRefNotFound
	}
	if err != nil {
		return "", err
	}
	if reference.Ref != ref {
		return "", errors.New("githubapp: reference identity changed")
	}
	object := reference.Object
	seen := map[string]bool{}
	for object.Type == "tag" && strings.HasPrefix(ref, "refs/tags/") {
		if !runspec.IsCommitID(object.SHA) || seen[object.SHA] {
			return "", errors.New("githubapp: invalid annotated tag chain")
		}
		seen[object.SHA] = true
		var tag struct {
			SHA    string    `json:"sha"`
			Object gitObject `json:"object"`
		}
		if err := c.getJSON(ctx, root+"/git/tags/"+url.PathEscape(object.SHA), auth, &tag); err != nil {
			return "", err
		}
		if tag.SHA != object.SHA {
			return "", errors.New("githubapp: tag identity changed")
		}
		object = tag.Object
	}
	if object.Type != "commit" || !runspec.IsCommitID(object.SHA) {
		return "", errors.New("githubapp: ref did not resolve to a commit")
	}
	return object.SHA, nil
}

// RepositoryFileSnapshot reads one file at the current default-branch head
// with a token restricted to this repository and contents:read. Found is false
// only when GitHub confirms the file is absent at that commit.
type RepositoryFileSnapshot struct {
	RepositoryID  int64
	DefaultBranch string
	HeadSHA       string
	Content       []byte
	Found         bool
}

func (c *Client) FileAtDefaultHead(ctx context.Context, installation int64, owner, repo, file string) (RepositoryFileSnapshot, error) {
	tok, err := c.installationToken(ctx, installation, []string{repo}, map[string]string{"contents": "read"})
	if err != nil {
		return RepositoryFileSnapshot{}, err
	}
	root := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	auth := "Bearer " + tok.Token
	var meta struct {
		ID            int64  `json:"id"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(ctx, root, auth, &meta); err != nil {
		return RepositoryFileSnapshot{}, err
	}
	if meta.ID <= 0 || meta.DefaultBranch == "" {
		return RepositoryFileSnapshot{}, errors.New("githubapp: repository has no id or default branch")
	}
	head, err := c.resolveCommit(ctx, root, auth, "refs/heads/"+meta.DefaultBranch)
	if err != nil {
		return RepositoryFileSnapshot{}, err
	}
	out := RepositoryFileSnapshot{RepositoryID: meta.ID, DefaultBranch: meta.DefaultBranch, HeadSHA: head}
	out.Content, out.Found, err = c.fileAt(ctx, auth, root, head, file)
	return out, err
}

// FileAtCommit reads one file at sha with a token restricted to repository and contents:read.
func (c *Client) FileAtCommit(ctx context.Context, installation, repository int64, owner, repo, sha, file string) ([]byte, bool, error) {
	tok, err := c.InstallationTokenByIDs(ctx, installation, []int64{repository}, map[string]string{"contents": "read"})
	if err != nil {
		return nil, false, err
	}
	return c.fileAt(ctx, "Bearer "+tok.Token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), sha, file)
}

func (c *Client) fileAt(ctx context.Context, auth, root, sha, file string) ([]byte, bool, error) {
	filePath := strings.TrimPrefix(file, "/")
	if filePath == "" || strings.Contains(filePath, "..") || strings.ContainsAny(filePath, "?#") {
		return nil, false, errors.New("githubapp: invalid repository file path")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+root+"/contents/"+filePath+"?ref="+url.QueryEscape(sha), nil)
	if err != nil {
		return nil, false, err
	}
	c.apiHeaders(req, auth)
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("githubapp: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, fmt.Errorf("%w: %s answered %d", ErrRejected, req.URL.Path, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, false, fmt.Errorf("githubapp: %s answered %d", req.URL.Path, resp.StatusCode)
	}
	// safety: one byte past the limit lets the configuration parser refuse an oversized file.
	content, err := io.ReadAll(io.LimitReader(resp.Body, limits.ProjectConfigBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("githubapp: read %s: %w", req.URL.Host, err)
	}
	return content, true, nil
}

// hack: GitHub answers a request for ungranted permissions with the same 422 as a
// repository the installation does not cover, so only the body tells them apart.
func permissionsNotGranted(body []byte) bool {
	var answer struct {
		Message string `json:"message"`
	}
	return json.Unmarshal(body, &answer) == nil &&
		strings.Contains(strings.ToLower(answer.Message), "permissions requested are not granted")
}

// CreateCommitStatus posts a commit status with an installation token.
func (c *Client) CreateCommitStatus(ctx context.Context, token, owner, repo, sha string, status any) error {
	_, err := c.repoWrite(ctx, http.MethodPost, token, owner, repo, "/statuses/"+url.PathEscape(sha), "commit status", status)
	return err
}

// CheckRunOutput is the text a check run shows on GitHub. GitHub accepts at
// most 65535 characters in Summary.
type CheckRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CheckRun is what the App writes to one check run. Status is "queued",
// "in_progress" or "completed"; Conclusion is set only with "completed".
type CheckRun struct {
	Name        string          `json:"name,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"`
	DetailsURL  string          `json:"details_url,omitempty"`
	ExternalID  string          `json:"external_id,omitempty"`
	Status      string          `json:"status"`
	Conclusion  string          `json:"conclusion,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Output      *CheckRunOutput `json:"output,omitempty"`
}

// CreateCheckRun creates a check run on owner/repo with an installation
// token and returns its id. A token without checks:write is
// [ErrPermissionMissing].
func (c *Client) CreateCheckRun(ctx context.Context, token, owner, repo string, run CheckRun) (int64, error) {
	raw, err := c.repoWrite(ctx, http.MethodPost, token, owner, repo, "/check-runs", "create check run", run)
	if err != nil {
		return 0, err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &created) != nil || created.ID <= 0 {
		return 0, errors.New("githubapp: github returned no check run id")
	}
	return created.ID, nil
}

// FindCheckRun returns the id of this App's check run named name on commit
// sha whose external id is externalID, or 0 when there is none.
func (c *Client) FindCheckRun(ctx context.Context, token, owner, repo, sha, name, externalID string) (int64, error) {
	q := url.Values{
		"check_name": {name}, "filter": {"all"}, "per_page": {"100"},
		"app_id": {strconv.FormatInt(c.cfg.AppID, 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+"/repos/"+url.PathEscape(owner)+"/"+
		url.PathEscape(repo)+"/commits/"+url.PathEscape(sha)+"/check-runs?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	c.apiHeaders(req, "token "+token)
	var found struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			ExternalID string `json:"external_id"`
		} `json:"check_runs"`
	}
	status, err := c.do(req, &found)
	switch {
	case err != nil:
		return 0, err
	case status == http.StatusForbidden:
		return 0, fmt.Errorf("%w: find check run answered 403", ErrPermissionMissing)
	case status < 200 || status >= 300:
		return 0, &apiError{op: "find check run", status: status}
	}
	for _, run := range found.CheckRuns {
		if run.ExternalID == externalID && run.ID > 0 {
			return run.ID, nil
		}
	}
	return 0, nil
}

// UpdateCheckRun updates check run id on owner/repo. GitHub keeps the name
// and head commit the run was created with.
func (c *Client) UpdateCheckRun(ctx context.Context, token, owner, repo string, id int64, run CheckRun) error {
	run.HeadSHA = ""
	_, err := c.repoWrite(ctx, http.MethodPatch, token, owner, repo,
		"/check-runs/"+strconv.FormatInt(id, 10), "update check run", run)
	return err
}

func (c *Client) repoWrite(ctx context.Context, method, token, owner, repo, path, op string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method,
		c.cfg.APIURL+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.apiHeaders(req, "token "+token)
	req.Header.Set("Content-Type", "application/json")
	status, raw, err := c.send(req)
	switch {
	case err != nil:
		return nil, err
	case status == http.StatusForbidden:
		return nil, fmt.Errorf("%w: %s answered 403", ErrPermissionMissing, op)
	case status < 200 || status >= 300:
		return nil, &apiError{op: op, status: status}
	}
	return raw, nil
}

var errNotFound = errors.New("githubapp: not found")

func (c *Client) getJSON(ctx context.Context, path, authorization string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+path, nil)
	if err != nil {
		return err
	}
	c.apiHeaders(req, authorization)
	status, err := c.do(req, out)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound:
		return errNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: %s answered %d", ErrRejected, req.URL.Path, status)
	case status != http.StatusOK:
		return fmt.Errorf("githubapp: %s answered %d", req.URL.Path, status)
	}
	return nil
}

func (c *Client) apiHeaders(req *http.Request, authorization string) {
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "sparkwing-controller")
}

func (c *Client) do(req *http.Request, out any) (int, error) {
	status, body, err := c.send(req)
	if err != nil {
		return 0, err
	}
	if out != nil && len(body) > 0 && status < 300 {
		if err := json.Unmarshal(body, out); err != nil {
			return 0, fmt.Errorf("githubapp: unreadable answer from %s: %w", req.URL.Path, err)
		}
	}
	return status, nil
}

func (c *Client) send(req *http.Request) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
	defer cancel()
	resp, err := c.cfg.HTTP.Do(req.WithContext(ctx))
	if err != nil {
		return 0, nil, fmt.Errorf("githubapp: %s: %w", req.URL.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("githubapp: read %s: %w", req.URL.Host, err)
	}
	return resp.StatusCode, body, nil
}

func (c *Client) appJWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-jwtBackdate).Unix(),
		"exp": now.Add(jwtLifetime).Unix(),
		"iss": strconv.FormatInt(c.cfg.AppID, 10),
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.cfg.PrivateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
