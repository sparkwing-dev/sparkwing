package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
)

func CapabilitiesHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caps, err := b.Capabilities(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, caps)
	}
}

// safety: teams and sign-in providers are the controller's to announce, so a
// dashboard without a controller session backend reports neither and renders as before.
func dashboardCapabilitiesHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caps, err := opts.Backend.Capabilities(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if controllerURL := authControllerURL(opts); controllerURL != "" {
			// safety: an unreachable controller leaves the identity fields out, which renders the single-team dashboard.
			if identity, err := controllerIdentityCapabilities(r.Context(), controllerURL, sessionIDFromContext(r.Context())); err == nil {
				caps.Teams = identity.Teams
				caps.Billing = identity.Billing
				caps.Auth = identity.Auth
				caps.GitHubApp = identity.GitHubApp
			}
		}
		writeJSON(w, http.StatusOK, caps)
	}
}

type identityCapabilities struct {
	Teams     *backend.CapabilitiesTeams     `json:"teams"`
	Billing   *backend.CapabilitiesBilling   `json:"billing"`
	Auth      *backend.CapabilitiesAuth      `json:"auth"`
	GitHubApp *backend.CapabilitiesGitHubApp `json:"github_app"`
}

func controllerIdentityCapabilities(ctx context.Context, controllerURL, sessionID string) (identityCapabilities, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(controllerURL, "/")+"/api/v1/capabilities", nil)
	if err != nil {
		return identityCapabilities{}, err
	}
	if sessionID != "" {
		req.Header.Set("Authorization", sessionAuthorization(sessionID))
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return identityCapabilities{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return identityCapabilities{}, fmt.Errorf("controller capabilities: status %d", resp.StatusCode)
	}
	var out identityCapabilities
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return identityCapabilities{}, fmt.Errorf("decode controller capabilities: %w", err)
	}
	return out, nil
}

// safety: a single-team controller offers no provider, so its sign-in page stays
// password-only, and so does a dashboard that would not serve the account
// session a provider signs in.
func withSignInProviders(ctx context.Context, opts HandlerOptions, data loginPageData) loginPageData {
	controllerURL := authControllerURL(opts)
	if controllerURL == "" || !accountSessionsServed(opts) {
		return data
	}
	caps, err := controllerIdentityCapabilities(ctx, controllerURL, "")
	if err != nil || caps.Teams == nil || !caps.Teams.Enabled || caps.Auth == nil {
		return data
	}
	data.Google = slices.Contains(caps.Auth.Providers, "google")
	data.GitHub = slices.Contains(caps.Auth.Providers, "github")
	return data
}
