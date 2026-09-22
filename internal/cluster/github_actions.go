package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// githubCredential is what the controller hands a GitHub Actions job in
// exchange for the job's ID token.
type githubCredential struct {
	Token      string   `json:"token"`
	Team       string   `json:"team"`
	Repository string   `json:"repository"`
	ExpiresAt  int64    `json:"expires_at"`
	Labels     []string `json:"labels"`
}

const githubExchangeTimeout = 30 * time.Second

// githubActionsCredential requests this job's ID token with the controller
// as its audience and exchanges it for a runner credential bound to team and
// to the repository the job runs in.
func githubActionsCredential(ctx context.Context, client *http.Client, controllerURL, team string) (githubCredential, error) {
	requestURL := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	requestToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if requestURL == "" || requestToken == "" {
		return githubCredential{}, errors.New("no GitHub Actions ID token is available; " +
			"run inside a GitHub Actions job whose workflow grants `permissions: id-token: write`")
	}
	if strings.TrimSpace(team) == "" {
		return githubCredential{}, errors.New("--team names the Sparkwing team this repository's work belongs to")
	}
	ctx, cancel := context.WithTimeout(ctx, githubExchangeTimeout)
	defer cancel()
	audience := strings.TrimRight(controllerURL, "/")

	u, err := url.Parse(requestURL)
	if err != nil {
		return githubCredential{}, fmt.Errorf("ACTIONS_ID_TOKEN_REQUEST_URL: %w", err)
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return githubCredential{}, err
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	req.Header.Set("Accept", "application/json")
	var idToken struct {
		Value string `json:"value"`
	}
	if err := doJSON(client, req, http.StatusOK, &idToken); err != nil {
		return githubCredential{}, fmt.Errorf("request the job's ID token: %w", err)
	}
	if idToken.Value == "" {
		return githubCredential{}, errors.New("request the job's ID token: GitHub returned no token")
	}

	body, err := json.Marshal(map[string]string{"id_token": idToken.Value, "team": team})
	if err != nil {
		return githubCredential{}, err
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, audience+"/api/v1/runners/github/exchange", bytes.NewReader(body))
	if err != nil {
		return githubCredential{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	var cred githubCredential
	if err := doJSON(client, req, http.StatusCreated, &cred); err != nil {
		return githubCredential{}, fmt.Errorf("exchange the ID token with the controller: %w", err)
	}
	if cred.Token == "" || cred.ExpiresAt == 0 {
		return githubCredential{}, errors.New("exchange the ID token with the controller: no credential in the answer")
	}
	return cred, nil
}

func doJSON(client *http.Client, req *http.Request, want int, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		var problem struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		detail := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &problem) == nil && problem.Message+problem.Error != "" {
			detail = strings.TrimSpace(problem.Message + " " + problem.Error)
		}
		return fmt.Errorf("%s answered %d: %s", req.URL.Host, resp.StatusCode, detail)
	}
	return json.Unmarshal(raw, out)
}

// githubClaimDeadline is when a job stops taking new nodes, leaving the
// credential's last minutes for the node it already holds.
func githubClaimDeadline(cred githubCredential) time.Time {
	const reserve = 10 * time.Minute
	return time.Unix(cred.ExpiresAt, 0).Add(-reserve)
}

func withLabel(labels []string, label string) []string {
	for _, l := range labels {
		if l == label {
			return labels
		}
	}
	return append(labels, label)
}
