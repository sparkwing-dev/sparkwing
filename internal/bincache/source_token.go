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

// ErrNoSourceToken reports a controller that mints no source token for the
// run: it has no GitHub App, or no installation the run's team holds covers
// the run's repository. The runner fetches with its own credentials.
var ErrNoSourceToken = errors.New("the controller mints no source token for this run")

// RequestSourceToken asks the controller for a short-lived GitHub token that
// reads the run's repository, for a runner holding a claim on runID.
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
	cli := &http.Client{
		Timeout: 15 * time.Second,
		// safety: a redirect would carry the runner token to whatever origin it names.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cli.Do(req)
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

// DirectCredential is a per-run credential a direct fetch presents. The zero
// value presents nothing, so the fetch uses this process's own git
// credentials.
type DirectCredential struct {
	// GitHubToken reads a github.com repository over https. Git reads it from
	// a pipe the fetch inherits, through a credential helper scoped to
	// github.com, so it is never in an environment, a URL, a command line or a
	// file.
	GitHubToken string
}

func githubTokenRemote(remote string) string {
	if https := githubHTTPS(remote); https != "" {
		return https
	}
	if strings.HasPrefix(strings.ToLower(remote), "https://github.com/") {
		return remote
	}
	return ""
}

// githubTokenScope is the only URL prefix git hands the source token to.
const githubTokenScope = "https://github.com/"

// credentialFD is the descriptor the fetch inherits the token on: the first
// of exec.Cmd.ExtraFiles.
const credentialFD = 3

// safety: the helper answers only git's get, from the inherited pipe, so a
// store or erase writes the token nowhere; the empty helper before it drops
// every helper the ambient config names for the scope.
const credentialHelper = `!f() { test "$1" != get || cat <&3; }; f`

// withGitHubCredential scopes the pipe's credential to scope in git config
// carried by the environment. The config names only the helper; the token
// itself travels on [credentialFD].
func withGitHubCredential(env []string, scope string) []string {
	count := 0
	out := make([]string, 0, len(env)+5)
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
	key := "credential." + strings.TrimRight(scope, "/") + ".helper"
	for _, value := range []string{"", credentialHelper} {
		idx := strconv.Itoa(count)
		out = append(out, "GIT_CONFIG_KEY_"+idx+"="+key, "GIT_CONFIG_VALUE_"+idx+"="+value)
		count++
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

// credentialPipe is the read end a fetch inherits as [credentialFD], already
// holding the token's credential and closed for writing, so the helper reads
// it once and then sees the end.
func credentialPipe(tok string) (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	_, werr := io.WriteString(w, "username=x-access-token\npassword="+tok+"\n")
	cerr := w.Close()
	if werr != nil || cerr != nil {
		_ = r.Close()
		return nil, errors.Join(werr, cerr)
	}
	return r, nil
}

// GitHubAppSourceEnabled reports whether this runner asks its controller for a
// source token before a direct fetch of a GitHub repository.
func GitHubAppSourceEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(GitHubAppSourceEnv)))
	return v == "1" || v == "true" || v == "yes"
}

// GitHubAppSourceEnv turns on source tokens for a runner: `sparkwing-runner
// runner --github-app-source` sets it for the loops it starts.
const GitHubAppSourceEnv = "SPARKWING_GITHUB_APP_SOURCE"

// DirectCredentialFor asks the controller for the run's source token when this
// runner opted in and repoURL is a GitHub repository. Any failure falls back
// to the zero credential, which fetches with this process's own credentials.
func DirectCredentialFor(ctx context.Context, controllerURL, runnerToken, runID, repoURL string) (DirectCredential, error) {
	if !GitHubAppSourceEnabled() || githubTokenRemote(repoURL) == "" {
		return DirectCredential{}, nil
	}
	tok, err := RequestSourceToken(ctx, controllerURL, runnerToken, runID)
	if err != nil {
		return DirectCredential{}, err
	}
	return DirectCredential{GitHubToken: tok}, nil
}
