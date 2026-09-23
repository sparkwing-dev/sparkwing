package bincache

import (
	"context"
	"encoding/base64"
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

// safety: the token becomes a git config value through the environment, so
// anything but a token's own alphabet could smuggle in another setting.
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
	// GitHubToken reads a github.com repository over https. It reaches git
	// as an http extraheader in the fetch's environment, never in the URL or
	// on a command line.
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

// safety: the key is scoped to https://github.com/, so git sends the token to no other host.
func withGitHubToken(env []string, tok string) []string {
	count := 0
	out := make([]string, 0, len(env)+3)
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
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	idx := strconv.Itoa(count)
	return append(out,
		"GIT_CONFIG_KEY_"+idx+"=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_"+idx+"=AUTHORIZATION: basic "+basic,
		"GIT_CONFIG_COUNT="+strconv.Itoa(count+1),
	)
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
