package controller

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestClaimRun_PlanRefusedWhenARequiredSecretIsMissing(t *testing.T) {
	e, tenant, h, raw, body := controllerPlanFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "work"}}}}})
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []store.Secret{{Name: "TEAM_ONLY", Value: "private-team", Masked: true}, {Name: "ELSEWHERE", Value: "private-elsewhere", Pipeline: "release", Masked: true}} {
		sealed, err := cipher.SealBound(string(tenant.Team()), secret.Name, secret.Pipeline, secret.Shared, secret.Masked, secret.Value)
		if err != nil {
			t.Fatal(err)
		}
		secret.Value = sealed
		if err := teststore.SeedSecret(e.Store, tenant.Team(), secret, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var submission controllerwire.PlanSubmission
	if err := json.Unmarshal(body, &submission); err != nil {
		t.Fatal(err)
	}
	submission.Resolved.SecretNames = []string{"DEPLOY_TOKEN", "TEAM_ONLY", "ELSEWHERE"}
	body, err = json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	w := engineClaimHTTP(t, h, raw, "POST", "/api/v1/runs/run/plan", string(body), http.StatusUnprocessableEntity)
	var failure apierr.Envelope
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != apierr.PlanInvalid {
		t.Fatalf("preflight error=%s: %v", w.Body, err)
	}
	want := `pipeline "ci" did not start: this run cannot read 3 required secrets it declares: DEPLOY_TOKEN, TEAM_ONLY, ELSEWHERE. A team member sees which scope each is missing from, and the command that sets it, with: sparkwing runs status --run run --profile <profile>`
	if failure.Error.Message != want {
		t.Fatalf("planner refusal = %s, want %s", failure.Error.Message, want)
	}
	for _, leak := range []string{"team-wide", "shared", "release", "only for", "not set", "admin", "private-team", "private-elsewhere", "SLACK_HOOK"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("planner refusal discloses %q: %s", leak, w.Body)
		}
	}

	principal, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.Store.DelegateCapability(t.Context(), capability.DelegateSpec{Parent: principal.Authority, Audience: capability.Controller, Binding: principal.Capability.Binding, Grants: []capability.Grant{capability.SourceRead}, Repositories: principal.Capability.Repositories, Data: principal.Capability.Data, ExpiresAt: principal.Capability.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	property := func(alias, stream, delegated bool) bool {
		selector, token, path := "run", raw, "/events"
		if alias {
			selector = "1"
		}
		if stream {
			path += "/stream"
		}
		if delegated {
			token = child.Raw
		}
		request := httptest.NewRequest("GET", "/api/v1/runs/"+selector+path+"?include=secret_values", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-Sparkwing-Scopes", "runs.read,team.admin")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response.Code == http.StatusForbidden && !strings.Contains(response.Body.String(), "release") && !strings.Contains(response.Body.String(), "private-team")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}

	server := New(e.Store, nil).WithSecretsCipher(cipher).WithLicense(accountSessionTestLicense(t))
	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('reader','reader@example.test',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','reader','reader',1)`)
	session, csrf, _, err := e.Store.CreateAccountSession(t.Context(), store.Account{ID: "reader"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	w = reviewSessionRequest(t, server, session, csrf, "GET", "/api/v1/runs/run/events", "", http.StatusOK)

	for _, line := range []string{"TEAM_ONLY is set team-wide but not shared with runs", `ELSEWHERE is set only for pipeline \"release\"`, "sparkwing secrets set --name DEPLOY_TOKEN", "--pipeline ci"} {
		if !strings.Contains(w.Body.String(), line) {
			t.Fatalf("team diagnostic lacks %q: %s", line, w.Body)
		}
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), "run")
	if err != nil || snapshot.Run.Spec != nil || len(snapshot.Nodes) != 0 {
		t.Fatalf("refused plan persisted work: %+v: %v", snapshot, err)
	}
}
