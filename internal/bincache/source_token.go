package bincache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrNoSourceCredential reports a run the controller has no credential for:
// no GitHub App installation of the run's team covers its repository, and the
// team stored no git credential for its host. Its message names both remedies.
var ErrNoSourceCredential = errors.New("the controller holds no source credential for this run")

// bug: A controller predating the git-credential route answers a plain 404, distinct from no credential.
var errCredentialRouteAbsent = errors.New("the controller serves no git-credential route")

// Kinds of [DirectCredential] the controller releases.
const (
	CredentialGitHubApp = "github_app"
	CredentialSSH       = "ssh"
	CredentialHTTPS     = "https"
)

// DirectCredential is a per-run credential a direct fetch presents. The zero
// value presents nothing, so the fetch uses this process's own git
// credentials; only a runner its owner fenced with --allow-repo fetches that
// way.
type DirectCredential struct {
	// Kind is one of CredentialGitHubApp, CredentialSSH or CredentialHTTPS.
	Kind string
	// Host is the host the credential is bound to; the fetch refuses any
	// other.
	Host string
	// Username and Secret authenticate an https fetch: the App token under
	// x-access-token, or the team's stored token. For ssh, Secret is the
	// private key.
	Username string
	Secret   string
	// KnownHosts is the ssh host key the team's owner confirmed, the only
	// key the fetch trusts.
	KnownHosts string
	// ExtraRepositories are the repositories, as owner/name, a team owner
	// listed for the run's repository. An App token reads them too, and the
	// runner checks out submodules only when there are some.
	ExtraRepositories []string
}

// Empty reports the zero credential.
func (c DirectCredential) Empty() bool { return c.Kind == "" }

type gitCredentialBody struct {
	Kind              string   `json:"kind"`
	Host              string   `json:"host"`
	Token             string   `json:"token"`
	Username          string   `json:"username"`
	Secret            string   `json:"secret"`
	KnownHosts        string   `json:"known_hosts"`
	ExtraRepositories []string `json:"extra_repositories"`
	Error             string   `json:"error"`
	Message           string   `json:"message"`
}

// RequestDirectCredential asks the controller for the credential a runner
// holding a claim on runID fetches the run's source with. The controller
// decides which: the team's GitHub App token when an installation covers the
// repository, else the git credential the team stored for the repository's
// host. A controller from before the route is asked for its App source token
// instead.
func RequestDirectCredential(ctx context.Context, controllerURL, runnerToken, runID string) (DirectCredential, error) {
	if controllerURL == "" || runID == "" {
		return DirectCredential{}, fmt.Errorf("%w: no controller to ask", ErrNoSourceCredential)
	}
	cred, err := requestGitCredential(ctx, controllerURL, runnerToken, runID)
	if !errors.Is(err, errCredentialRouteAbsent) {
		return cred, err
	}
	tok, err := RequestSourceToken(ctx, controllerURL, runnerToken, runID)
	if err != nil {
		return DirectCredential{}, err
	}
	return DirectCredential{Kind: CredentialGitHubApp, Host: "github.com", Username: "x-access-token", Secret: tok}, nil
}

func requestGitCredential(ctx context.Context, controllerURL, runnerToken, runID string) (DirectCredential, error) {
	endpoint := strings.TrimRight(controllerURL, "/") + "/api/v1/runs/" + neturl.PathEscape(runID) + "/git-credential"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return DirectCredential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if runnerToken != "" {
		req.Header.Set("Authorization", "Bearer "+runnerToken)
	}
	resp, err := credentialHTTPClient().Do(req)
	if err != nil {
		return DirectCredential{}, fmt.Errorf("request git credential: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return DirectCredential{}, fmt.Errorf("request git credential: %w", err)
	}
	var body gitCredentialBody
	decodeErr := json.Unmarshal(raw, &body)
	switch {
	case resp.StatusCode == http.StatusMethodNotAllowed,
		resp.StatusCode == http.StatusNotFound && (decodeErr != nil || body.Error == ""):
		return DirectCredential{}, errCredentialRouteAbsent
	case resp.StatusCode == http.StatusNotFound && body.Error == "no_source_credential":
		return DirectCredential{}, fmt.Errorf("%w: %s", ErrNoSourceCredential, body.Message)
	case resp.StatusCode != http.StatusOK:
		msg := body.Message
		if msg == "" {
			msg = body.Error
		}
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 512 {
				msg = msg[:512]
			}
		}
		return DirectCredential{}, fmt.Errorf("request git credential: %s: %s", resp.Status, msg)
	case decodeErr != nil:
		return DirectCredential{}, fmt.Errorf("request git credential: decode: %w", decodeErr)
	}
	cred := DirectCredential{
		Kind: body.Kind, Host: strings.ToLower(body.Host), Username: body.Username,
		Secret: body.Secret, KnownHosts: body.KnownHosts, ExtraRepositories: body.ExtraRepositories,
	}
	if cred.Kind == CredentialGitHubApp {
		cred.Username, cred.Secret = "x-access-token", body.Token
	}
	if err := cred.validate(); err != nil {
		return DirectCredential{}, fmt.Errorf("request git credential: %w", err)
	}
	return cred, nil
}

func credentialHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second,
		// safety: a redirect would carry the runner token to whatever origin it names.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// safety: every field lands in git's credential protocol, an ssh key file or
// a known_hosts file, so each is held to a shape that cannot smuggle in
// another attribute, option or host.
func (c DirectCredential) validate() error {
	if !validCredentialHost(c.Host) {
		return fmt.Errorf("the controller answered with an unusable host %q", c.Host)
	}
	switch c.Kind {
	case CredentialGitHubApp:
		if c.Host != "github.com" || !plainToken(c.Secret) {
			return errors.New("the controller answered with no usable App token")
		}
	case CredentialHTTPS:
		if !plainCredentialValue(c.Username) || !plainCredentialValue(c.Secret) {
			return errors.New("the controller answered with no usable https credential")
		}
	case CredentialSSH:
		if !strings.Contains(c.Secret, "PRIVATE KEY") || len(c.Secret) > 16<<10 {
			return errors.New("the controller answered with no usable ssh key")
		}
		if c.KnownHosts == "" || strings.ContainsAny(c.KnownHosts, "\x00\r") || len(c.KnownHosts) > 16<<10 {
			return errors.New("the controller answered with no pinned host key")
		}
	default:
		return fmt.Errorf("the controller answered with an unknown credential kind %q", c.Kind)
	}
	if len(c.ExtraRepositories) > maxExtraRepositories {
		return errors.New("the controller answered with too many extra repositories")
	}
	for _, repo := range c.ExtraRepositories {
		if !validRepoSlug(repo) {
			return fmt.Errorf("the controller answered with an unusable extra repository %q", repo)
		}
	}
	return nil
}

const maxExtraRepositories = 10

func validRepoSlug(slug string) bool {
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" || len(slug) > 200 || strings.HasPrefix(owner, ".") || strings.HasPrefix(name, ".") {
		return false
	}
	for _, c := range owner + name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '.' && c != '_' {
			return false
		}
	}
	return true
}

func validCredentialHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, c := range host {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return !strings.HasPrefix(host, "-") && !strings.HasPrefix(host, ".")
}

func plainCredentialValue(v string) bool {
	if v == "" || len(v) > 1024 {
		return false
	}
	for _, c := range v {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// ErrNoSourceToken reports a controller that mints no source token for the
// run: it has no GitHub App, or no installation the run's team holds covers
// the run's repository.
var ErrNoSourceToken = errors.New("the controller mints no source token for this run")

// RequestSourceToken asks the controller for a short-lived GitHub token that
// reads the run's repository, for a runner holding a claim on runID. It is
// the route a controller from before the git-credential route serves.
func RequestSourceToken(ctx context.Context, controllerURL, runnerToken, runID string) (string, error) {
	if controllerURL == "" || runID == "" {
		return "", ErrNoSourceToken
	}
	endpoint := strings.TrimRight(controllerURL, "/") + "/api/v1/runs/" + neturl.PathEscape(runID) + "/source-token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	if runnerToken != "" {
		req.Header.Set("Authorization", "Bearer "+runnerToken)
	}
	resp, err := credentialHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("request source token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return "", ErrNoSourceToken
	case resp.StatusCode != http.StatusOK:
		msg, err := io.ReadAll(io.LimitReader(resp.Body, 512))
		if err != nil {
			return "", fmt.Errorf("request source token: %s", resp.Status)
		}
		return "", fmt.Errorf("request source token: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("request source token: decode: %w", err)
	}
	if !plainToken(body.Token) {
		return "", errors.New("request source token: the controller answered with no usable token")
	}
	return body.Token, nil
}

// safety: the token becomes a line of git's credential protocol, so anything
// but a token's own alphabet could smuggle in another attribute.
func plainToken(tok string) bool {
	if tok == "" || len(tok) > 512 {
		return false
	}
	for _, c := range tok {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

const credentialFD = 3

// safety: The pipe answers only git get requests; store and erase persist nothing, and ambient helpers are cleared.
const credentialHelper = `!f() { test "$1" = get || return 0; IFS= read -r u <&3 && IFS= read -r p <&3 && printf '%s\n%s\n' "$u" "$p"; }; f`

// safety: Git config names the pipe helper, but the credential itself stays on the inherited descriptor.
func withPipeCredential(env []string, scope string) []string {
	key := "credential." + strings.TrimRight(scope, "/") + ".helper"
	return withGitConfig(env, key, "", key, credentialHelper)
}

func withGitConfig(env []string, pairs ...string) []string {
	count := 0
	out := make([]string, 0, len(env)+len(pairs)+1)
	for _, item := range env {
		name, value, _ := strings.Cut(item, "=")
		if name == "GIT_CONFIG_COUNT" {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				count = n
			}
			continue
		}
		out = append(out, item)
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		idx := strconv.Itoa(count)
		out = append(out, "GIT_CONFIG_KEY_"+idx+"="+pairs[i], "GIT_CONFIG_VALUE_"+idx+"="+pairs[i+1])
		count++
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

// safety: Each git process consumes its own whole credential answer from the inherited pipe.
func credentialPipe(username, secret string, times int) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	_, werr := io.WriteString(w, strings.Repeat("username="+username+"\npassword="+secret+"\n", max(times, 1)))
	cerr := w.Close()
	if werr != nil || cerr != nil {
		_ = r.Close()
		return nil, errors.Join(werr, cerr)
	}
	return r, nil
}
