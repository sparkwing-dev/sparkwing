package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

// A local claim advertises the labels its controller resolved from machine
// configuration, never ones the request names, and both decide placement.
func TestLocalClaimAdvertisesConfiguredMachineLabels(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured []string
		requested  string
		awarded    bool
	}{
		{name: "configured", configured: []string{"host=moonborn"}, awarded: true},
		{name: "unconfigured", awarded: false},
		{name: "requested only", requested: `"labels":["host=moonborn"],`, awarded: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := engineRuntimeStore(t)
			engineSQL(t, e.Store, `UPDATE triggers SET needs_labels=? WHERE id='run'`, []byte(`["host=moonborn"]`))
			var body []byte
			if err := teststore.SQL(t, e.Store).QueryRow(`SELECT request_json FROM runs WHERE id='run'`).Scan(&body); err != nil {
				t.Fatal(err)
			}
			var sealed runspec.SealedRequest
			if err := json.Unmarshal(body, &sealed); err != nil {
				t.Fatal(err)
			}
			sealed.Request.Options.Requires = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"host=moonborn"}}}
			body, err := json.Marshal(sealed)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE id='run'`, body)
			e.claimProfiles = engineEnrollmentFunc(func(context.Context, capability.Principal) (match.Profile, error) {
				return match.Profile{Labels: tc.configured, OS: "linux", Arch: "amd64"}, nil
			})
			e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
			raw := engineHTTPRootAs(t, e, capability.SubjectLocal, "a", "local-peer", []capability.Grant{capability.TriggersClaim})
			h := e.Server.WithEngineController(e.EngineController).Handler()
			w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runners/claim", fmt.Sprintf(`{%s"runner_version":"v1","kinds":["trigger"]}`, tc.requested), 200)
			var response controllerwire.ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if (response.Claim != nil) != tc.awarded {
				t.Fatalf("awarded=%t, want %t: %s", response.Claim != nil, tc.awarded, w.Body)
			}
		})
	}
}
