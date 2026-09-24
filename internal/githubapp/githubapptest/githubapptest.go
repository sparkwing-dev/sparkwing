// Package githubapptest runs a local stand-in for the parts of GitHub a
// Sparkwing GitHub App talks to: App JWT authentication, installations,
// installation tokens restricted to repositories, the user authorization
// code exchange, organization memberships, commit statuses and check runs. It enforces
// what GitHub enforces, so a suite can prove the controller asks for no more
// than it should.
package githubapptest

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
)

// AppID is the App the fake serves.
const AppID = 4242

// WebhookSecret signs the deliveries [GitHub.Sign] produces.
const WebhookSecret = "fake-app-webhook-secret"

// Repo is a repository an installation covers.
type Repo struct {
	ID            int64
	FullName      string
	Private       bool
	DefaultBranch string
}

// Installation is one installation of the App.
type Installation struct {
	ID        int64
	Account   githubapp.Account
	Repos     []Repo
	Suspended bool
	// NoChecks is an installation whose owner has not accepted the App's
	// checks permission: GitHub refuses to mint a token that asks for it.
	NoChecks bool
}

// MintedToken records one installation token the fake issued.
type MintedToken struct {
	Installation int64
	Repositories []string
	Permissions  map[string]string
}

// Status records one commit status the fake accepted.
type Status struct {
	Repo    string
	SHA     string
	State   string
	Context string
}

// CheckRunCall records one check run write the fake accepted. An update
// carries the name and head commit the check run was created with.
type CheckRunCall struct {
	Method     string
	ID         int64
	Repo       string
	Name       string
	HeadSHA    string
	Status     string
	Conclusion string
	DetailsURL string
	ExternalID string
	Title      string
	Summary    string
}

type userCode struct {
	user          githubapp.User
	challenge     string
	redirect      string
	used          bool
	userTokenOrgs map[string]githubapp.OrgMembership
}

type issuedToken struct {
	installation int64
	repos        map[string]bool
	permissions  map[string]string
	expires      time.Time
}

// GitHub is the fake.
type GitHub struct {
	URL              string
	Key              *rsa.PrivateKey
	t                testing.TB
	mu               sync.Mutex
	insts            map[int64]*Installation
	codes            map[string]*userCode
	users            map[string]*userCode
	tokens           map[string]*issuedToken
	commits          map[string]string
	files            map[string][]byte
	failContents     int
	failInstallRepos int
	minted           []MintedToken
	stats            []Status
	checks           []CheckRunCall
	// checkRuns maps a check run id to the call that created it.
	checkRuns  map[int64]CheckRunCall
	failChecks int
}

// New starts the fake and stops it when t ends.
func New(t testing.TB) *GitHub {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("githubapptest: generate key: %v", err)
	}
	g := &GitHub{
		Key: key, t: t,
		insts:     map[int64]*Installation{},
		codes:     map[string]*userCode{},
		users:     map[string]*userCode{},
		tokens:    map[string]*issuedToken{},
		commits:   map[string]string{},
		files:     map[string][]byte{},
		checkRuns: map[int64]CheckRunCall{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", g.handleAccessToken)
	mux.HandleFunc("GET /user", g.handleUser)
	mux.HandleFunc("GET /user/installations", g.handleUserInstallations)
	mux.HandleFunc("GET /user/memberships/orgs/{org}", g.handleMembership)
	mux.HandleFunc("GET /app/installations/{id}", g.appOnly(g.handleInstallation))
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", g.appOnly(g.handleMint))
	mux.HandleFunc("GET /repos/{owner}/{repo}/installation", g.appOnly(g.handleRepoInstallation))
	mux.HandleFunc("GET /repos/{owner}/{repo}", g.handleRepoMetadata)
	mux.HandleFunc("GET /repos/{owner}/{repo}/contents/{path...}", g.handleContents)
	mux.HandleFunc("GET /installation/repositories", g.handleInstallationRepos)
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{ref...}", g.handleCommit)
	mux.HandleFunc("POST /repos/{owner}/{repo}/statuses/{sha}", g.handleStatus)
	mux.HandleFunc("POST /repos/{owner}/{repo}/check-runs", g.handleCheckRun)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/check-runs/{id}", g.handleCheckRun)
	srv := httptest.NewServer(mux)
	g.URL = srv.URL
	t.Cleanup(srv.Close)
	return g
}

// Config is an App configuration that talks to this fake.
func (g *GitHub) Config() githubapp.Config {
	return githubapp.Config{
		AppID: AppID, Slug: "sparkwing-test", ClientID: "Iv1.fake", ClientSecret: "fake-client-secret",
		WebhookSecret: WebhookSecret, PrivateKey: g.Key, WebURL: g.URL, APIURL: g.URL,
	}
}

// AddInstallation registers inst.
func (g *GitHub) AddInstallation(inst Installation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cp := inst
	g.insts[inst.ID] = &cp
}

// SetRepos replaces the repositories installation id covers.
func (g *GitHub) SetRepos(id int64, repos ...Repo) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.insts[id].Repos = repos
}

// SetCommit makes a repository ref resolve to sha.
func (g *GitHub) SetCommit(repo, ref, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.commits[strings.ToLower(repo)+"/"+ref] = sha
}

// SetFile makes path available at sha, or removes it when content is nil.
func (g *GitHub) SetFile(repo, sha, path string, content []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.ToLower(repo) + "/" + sha + "/" + path
	if content == nil {
		delete(g.files, key)
		return
	}
	g.files[key] = append([]byte(nil), content...)
}

// FailContents makes the next n file reads fail as a GitHub API error.
func (g *GitHub) FailContents(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failContents = n
}

// FailInstallationRepositories makes the next n repository-list reads fail.
func (g *GitHub) FailInstallationRepositories(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failInstallRepos = n
}

func (g *GitHub) handleRepoMetadata(w http.ResponseWriter, r *http.Request) {
	repo := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	tok := g.installationToken(r)
	if tok == nil || !tok.repos[repo] || tok.permissions["contents"] != "read" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Forbidden"})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, candidate := range g.insts[tok.installation].Repos {
		if strings.EqualFold(candidate.FullName, repo) {
			branch := candidate.DefaultBranch
			if branch == "" {
				branch = "main"
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": candidate.ID, "default_branch": branch})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func (g *GitHub) handleContents(w http.ResponseWriter, r *http.Request) {
	repo := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	tok := g.installationToken(r)
	if tok == nil || !tok.repos[repo] || tok.permissions["contents"] != "read" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Forbidden"})
		return
	}
	g.mu.Lock()
	if g.failContents > 0 {
		g.failContents--
		g.mu.Unlock()
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Bad Gateway"})
		return
	}
	data, ok := g.files[repo+"/"+r.URL.Query().Get("ref")+"/"+r.PathValue("path")]
	g.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)})
}

func (g *GitHub) handleCommit(w http.ResponseWriter, r *http.Request) {
	repo := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	tok := g.installationToken(r)
	if tok == nil || !tok.repos[repo] || tok.permissions["contents"] != "read" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Forbidden"})
		return
	}
	g.mu.Lock()
	sha := g.commits[repo+"/"+r.PathValue("ref")]
	g.mu.Unlock()
	if sha == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sha": sha})
}

// SetChecksGranted records whether installation id's owner accepted the
// checks permission.
func (g *GitHub) SetChecksGranted(id int64, granted bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.insts[id].NoChecks = !granted
}

// FailCheckRuns makes the next n check run writes answer 502.
func (g *GitHub) FailCheckRuns(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failChecks = n
}

// CheckRunCalls lists the check run writes accepted so far.
func (g *GitHub) CheckRunCalls() []CheckRunCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]CheckRunCall(nil), g.checks...)
}

// IssueCode registers an authorization code GitHub would hand the browser of
// user after it authorized with verifier's challenge and redirectURI. orgs is
// the user's membership in each organization.
func (g *GitHub) IssueCode(code string, user githubapp.User, verifier, redirectURI string, orgs map[string]githubapp.OrgMembership) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.codes[code] = &userCode{
		user: user, challenge: googleauth.CodeChallenge(verifier), redirect: redirectURI, userTokenOrgs: orgs,
	}
}

// Minted lists the installation tokens issued so far.
func (g *GitHub) Minted() []MintedToken {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]MintedToken(nil), g.minted...)
}

// Statuses lists the commit statuses accepted so far.
func (g *GitHub) Statuses() []Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Status(nil), g.stats...)
}

// TokenCovers reports whether token is a live installation token that may
// read repo's contents.
func (g *GitHub) TokenCovers(token, repo string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	tok := g.tokens[token]
	return tok != nil && time.Now().Before(tok.expires) && tok.repos[strings.ToLower(repo)] &&
		tok.permissions["contents"] != ""
}

// Sign is the X-Hub-Signature-256 header GitHub sends with body.
func Sign(body []byte) string {
	return SignWith(WebhookSecret, body)
}

// SignWith signs body with secret.
func SignWith(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (g *GitHub) handleAccessToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c := g.codes[r.PostForm.Get("code")]
	reject := func(reason string) {
		writeJSON(w, http.StatusOK, map[string]string{"error": reason})
	}
	switch {
	case r.PostForm.Get("client_id") != "Iv1.fake" || r.PostForm.Get("client_secret") != "fake-client-secret":
		reject("incorrect_client_credentials")
	case c == nil || c.used:
		reject("bad_verification_code")
	case googleauth.CodeChallenge(r.PostForm.Get("code_verifier")) != c.challenge:
		reject("bad_verification_code")
	case r.PostForm.Get("redirect_uri") != c.redirect:
		reject("redirect_uri_mismatch")
	default:
		c.used = true
		tok := "ghu_" + randomHex()
		g.users[tok] = c
		writeJSON(w, http.StatusOK, map[string]string{"access_token": tok, "token_type": "bearer"})
	}
}

func (g *GitHub) userFor(r *http.Request) *userCode {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.users[raw]
}

func (g *GitHub) handleUser(w http.ResponseWriter, r *http.Request) {
	u := g.userFor(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	writeJSON(w, http.StatusOK, u.user)
}

func (g *GitHub) handleMembership(w http.ResponseWriter, r *http.Request) {
	u := g.userFor(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	m, ok := u.userTokenOrgs[r.PathValue("org")]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (g *GitHub) handleUserInstallations(w http.ResponseWriter, r *http.Request) {
	u := g.userFor(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	g.mu.Lock()
	var installations []map[string]any
	for _, inst := range g.insts {
		if inst.Account.Type == "User" && inst.Account.ID == u.user.ID ||
			inst.Account.Type == "Organization" && u.userTokenOrgs[inst.Account.Login].State == "active" {
			installations = append(installations, installationJSON(inst))
		}
	}
	g.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"installations": installations})
}

func (g *GitHub) appOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !g.validJWT(raw) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "A JSON web token could not be decoded"})
			return
		}
		next(w, r)
	}
}

func (g *GitHub) validJWT(raw string) bool {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(&g.Key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return false
	}
	now := time.Now().Unix()
	return claims.Iss == strconv.Itoa(AppID) && claims.Iat <= now && claims.Exp > now && claims.Exp-claims.Iat <= 600
}

func (g *GitHub) installation(r *http.Request) *Installation {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.insts[id]
}

func installationJSON(inst *Installation) map[string]any {
	out := map[string]any{
		"id":                   inst.ID,
		"app_id":               AppID,
		"account":              inst.Account,
		"repository_selection": "selected",
		"suspended_at":         nil,
	}
	if inst.Suspended {
		out["suspended_at"] = time.Now().UTC().Format(time.RFC3339)
	}
	return out
}

func (g *GitHub) handleInstallation(w http.ResponseWriter, r *http.Request) {
	inst := g.installation(r)
	if inst == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, installationJSON(inst))
}

func (g *GitHub) handleRepoInstallation(w http.ResponseWriter, r *http.Request) {
	full := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, inst := range g.insts {
		for _, repo := range inst.Repos {
			if strings.ToLower(repo.FullName) == full {
				writeJSON(w, http.StatusOK, installationJSON(inst))
				return
			}
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func (g *GitHub) handleMint(w http.ResponseWriter, r *http.Request) {
	inst := g.installation(r)
	if inst == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	if inst.Suspended {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "This installation has been suspended"})
		return
	}
	var req struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, checks := req.Permissions["checks"]; checks && inst.NoChecks {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "The permissions requested are not granted to this installation."})
		return
	}
	tok := &issuedToken{
		installation: inst.ID, repos: map[string]bool{}, permissions: req.Permissions,
		expires: time.Now().Add(time.Hour),
	}
	owner := inst.Account.Login
	for _, name := range req.Repositories {
		found := false
		for _, repo := range inst.Repos {
			if strings.EqualFold(repo.FullName, owner+"/"+name) {
				found = true
			}
		}
		if !found {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "There is at least one repository that does not exist or is not accessible to the parent installation."})
			return
		}
		tok.repos[strings.ToLower(owner+"/"+name)] = true
	}
	if len(req.Repositories) == 0 {
		for _, repo := range inst.Repos {
			tok.repos[strings.ToLower(repo.FullName)] = true
		}
	}
	raw := "ghs_" + randomHex()
	g.tokens[raw] = tok
	g.minted = append(g.minted, MintedToken{Installation: inst.ID, Repositories: req.Repositories, Permissions: req.Permissions})
	writeJSON(w, http.StatusCreated, map[string]any{"token": raw, "expires_at": tok.expires.UTC().Format(time.RFC3339)})
}

func (g *GitHub) installationToken(r *http.Request) *issuedToken {
	auth := r.Header.Get("Authorization")
	raw, ok := strings.CutPrefix(auth, "token ")
	if !ok {
		raw, ok = strings.CutPrefix(auth, "Bearer ")
	}
	if !ok {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	tok := g.tokens[raw]
	if tok == nil || time.Now().After(tok.expires) {
		return nil
	}
	return tok
}

func (g *GitHub) handleInstallationRepos(w http.ResponseWriter, r *http.Request) {
	tok := g.installationToken(r)
	if tok == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failInstallRepos > 0 {
		g.failInstallRepos--
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Bad Gateway"})
		return
	}
	inst := g.insts[tok.installation]
	repos := []map[string]any{}
	for _, repo := range inst.Repos {
		repos = append(repos, map[string]any{"id": repo.ID, "full_name": repo.FullName, "private": repo.Private})
	}
	writeJSON(w, http.StatusOK, map[string]any{"total_count": len(repos), "repositories": repos})
}

func (g *GitHub) handleStatus(w http.ResponseWriter, r *http.Request) {
	tok := g.installationToken(r)
	full := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if tok == nil || !tok.repos[full] || tok.permissions["statuses"] != "write" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
		return
	}
	var req struct {
		State   string `json:"state"`
		Context string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	g.mu.Lock()
	g.stats = append(g.stats, Status{Repo: full, SHA: r.PathValue("sha"), State: req.State, Context: req.Context})
	g.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]string{"state": req.State})
}

func (g *GitHub) handleCheckRun(w http.ResponseWriter, r *http.Request) {
	tok := g.installationToken(r)
	full := strings.ToLower(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if tok == nil || !tok.repos[full] || tok.permissions["checks"] != "write" {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
		return
	}
	var req struct {
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		DetailsURL string `json:"details_url"`
		ExternalID string `json:"external_id"`
		Output     *struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failChecks > 0 {
		g.failChecks--
		writeJSON(w, http.StatusBadGateway, map[string]string{"message": "Server Error"})
		return
	}
	call := CheckRunCall{
		Method: "create", Repo: full, Name: req.Name, HeadSHA: req.HeadSHA, Status: req.Status,
		Conclusion: req.Conclusion, DetailsURL: req.DetailsURL, ExternalID: req.ExternalID,
	}
	if req.Output != nil {
		call.Title, call.Summary = req.Output.Title, req.Output.Summary
		// GitHub refuses a summary longer than this many characters.
		if utf8.RuneCountInString(call.Summary) > 65535 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "output.summary is too long"})
			return
		}
	}
	if (call.Status == "completed") != (call.Conclusion != "") {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "conclusion goes with status completed"})
		return
	}
	if r.Method == http.MethodPatch {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		created, ok := g.checkRuns[id]
		if err != nil || !ok || created.Repo != full {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
			return
		}
		call.Method, call.ID, call.Name, call.HeadSHA = "update", id, created.Name, created.HeadSHA
	} else {
		if call.Name == "" || len(call.HeadSHA) != 40 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "name and head_sha are required"})
			return
		}
		call.ID = int64(len(g.checkRuns) + 1000)
		g.checkRuns[call.ID] = call
	}
	g.checks = append(g.checks, call)
	status := http.StatusCreated
	if call.Method == "update" {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"id": call.ID, "status": call.Status})
}

func randomHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unrandom"
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}
