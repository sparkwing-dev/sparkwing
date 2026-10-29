package acceptance_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/googleauth"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
)

func TestOAuthHTTPLicenseControlsPublicProviders(t *testing.T) {
	pub, private := licensetest.NewKey(t)
	_, other := licensetest.NewKey(t)
	now := time.Now()
	terms := licensetest.Terms{Features: []string{license.FeatureMultiTeam}, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	expired, noFeature := terms, terms
	expired.ExpiresAt = now.Add(-time.Minute)
	noFeature.Features = []string{"billing"}
	valid := licensetest.Sign(t, private, terms)
	_, signature, ok := strings.Cut(valid, ".")
	if !ok {
		t.Fatal("signed license has no signature")
	}
	for name, raw := range map[string]string{
		"absent": "", "valid": valid,
		"wrong key":         licensetest.Sign(t, other, terms),
		"expired":           licensetest.Sign(t, private, expired),
		"no feature":        licensetest.Sign(t, private, noFeature),
		"changed signature": valid[:len(valid)-6] + "AAAAAA",
		"changed payload":   base64.RawURLEncoding.EncodeToString([]byte(`{"features":["multi-team"],"issued_to":"other","issued_at":"2026-01-01T00:00:00Z","expires_at":"2099-01-01T00:00:00Z"}`)) + "." + signature,
	} {
		t.Run(name, func(t *testing.T) {
			d := deployment.Config{}
			if raw != "" {
				var err error
				d, err = deployment.Parse(deployment.Inputs{Kind: string(deployment.KindTeam), License: raw, LicenseKey: pub, Now: now})
				if name != "valid" {
					if err == nil {
						t.Fatal("a team deployment started with this license")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			f := newDeploymentSessionFlow(t, d)
			p := &oauthProvider{}
			h := withOAuthGitHub(f.server, p).WithGoogleSignIn(googleauth.New(googleauth.Google("client", "private")), []string{oauthOrigin + "/auth/google/callback"}).Handler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
			var caps struct {
				Teams struct{ Enabled bool }
				Auth  struct{ Providers []string }
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil || caps.Teams.Enabled != (name == "valid") {
				t.Fatalf("capabilities HTTP %d %s", w.Code, w.Body)
			}
			if name == "valid" && !slices.Equal(caps.Auth.Providers, []string{"google", "github"}) || name != "valid" && len(caps.Auth.Providers) != 0 {
				t.Fatalf("providers = %v", caps.Auth.Providers)
			}
			for _, provider := range []string{"google", "github"} {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, oauthOrigin+"/auth/"+provider+"/start", nil))
				location, err := url.Parse(w.Header().Get("Location"))
				started := err == nil && location.Query().Get("state") != "" && location.Query().Get("code_challenge") != "" && oauthCookie(w, "__Host-sw_oauth") != nil
				if name == "valid" {
					started = started && map[string]string{"google": "accounts.google.com", "github": "provider.test"}[provider] == location.Host
				} else {
					started = location != nil && location.String() == "/login?refused=unavailable" && oauthCookie(w, "__Host-sw_oauth") == nil
				}
				if w.Code != 303 || p.calls != 0 || !started {
					t.Fatalf("%s start HTTP %d to %q %s", provider, w.Code, w.Header().Get("Location"), w.Body)
				}
			}
		})
	}
}
