package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ExchangeGitHubActions requests the job's ID token for this controller and exchanges it for push-bound access.
func ExchangeGitHubActions(ctx context.Context, options EngineOptions, team, requestURL, requestToken string) (capability.Issued, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return capability.Issued{}, err
	}
	endpoint, err := url.Parse(requestURL)
	if err != nil || endpoint == nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || requestToken == "" {
		return capability.Issued{}, errors.New("GitHub Actions requires ACTIONS_ID_TOKEN_REQUEST_URL and ACTIONS_ID_TOKEN_REQUEST_TOKEN; grant the workflow id-token: write")
	}
	query := endpoint.Query()
	query.Set("audience", c.base)
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return capability.Issued{}, err
	}
	request.Header.Set("Authorization", "Bearer "+requestToken)
	response, err := c.http.Do(request)
	if err != nil {
		return capability.Issued{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return capability.Issued{}, fmt.Errorf("GitHub Actions ID token request failed: HTTP %d", response.StatusCode)
	}
	var proof struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(response.Body).Decode(&proof); err != nil || proof.Value == "" {
		return capability.Issued{}, errors.New("GitHub Actions returned no ID token")
	}
	var issued struct {
		Token     string             `json:"token"`
		Team      string             `json:"team"`
		ExpiresAt int64              `json:"expires_at"`
		Binding   capability.Binding `json:"binding"`
	}
	input := struct {
		Team    string `json:"team"`
		IDToken string `json:"id_token"`
	}{team, proof.Value}
	if err := c.request(ctx, http.MethodPost, "/api/v1/runners/github/exchange", "/api/v1/runners/github/exchange", "", input, &issued); err != nil {
		return capability.Issued{}, err
	}
	expires := time.Unix(issued.ExpiresAt, 0)
	return capability.Issued{Raw: issued.Token, Capability: capability.Capability{Team: issued.Team, Binding: issued.Binding, ExpiresAt: &expires}}, nil
}
