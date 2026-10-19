package acceptance_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
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
			f := newOAuthHTTP(t)
			f.server.WithGoogleSignIn(googleauth.New(googleauth.Google("client", "private")), []string{oauthOrigin + "/auth/google/callback"})
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
			h := f.server.WithDeployment(d).Handler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/capabilities", nil))
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
				h.ServeHTTP(w, httptest.NewRequest("GET", oauthOrigin+"/auth/"+provider+"/start", nil))
				status := 404
				if name == "valid" {
					status = 303
				}
				if w.Code != status || f.provider.calls != 0 {
					t.Fatalf("%s start HTTP %d %s", provider, w.Code, w.Body)
				}
			}
		})
	}
}
