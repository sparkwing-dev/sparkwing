package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func TestPlanSecretPreflightNamesUnreadableScopes(t *testing.T) {
	tenant, request, now := planFixture(t, runspec.DispatchHost)
	for _, secret := range []Secret{{Name: "TEAM_ONLY", Value: "valid-secret"}, {Name: "ELSEWHERE", Value: "valid-secret", Pipeline: "release"}, {Name: "OWN", Value: "valid-secret", Pipeline: "p"}, {Name: "SHARED", Value: "valid-secret", Shared: true}} {
		if err := seedSecret(t, tenant, secret, *now); err != nil {
			t.Fatal(err)
		}
	}
	request.Seal.Resolved.SecretNames = []string{"OWN", "SHARED", "ABSENT", "TEAM_ONLY", "ELSEWHERE"}
	_, err := tenant.AcceptPlan(t.Context(), request)
	var refused *SecretPreflightError
	if !errors.As(err, &refused) {
		t.Fatalf("preflight: %v", err)
	}
	var canonical *apierr.Error
	if !errors.As(err, &canonical) || canonical.Code != apierr.PlanInvalid {
		t.Fatalf("preflight lacks canonical plan_invalid: %v", err)
	}
	for _, want := range []string{"ABSENT is not set", "TEAM_ONLY is set team-wide but not shared", `ELSEWHERE is set only for pipeline "release"`, "sparkwing secrets set", "--pipeline p", "--shared"} {
		if !strings.Contains(refused.Detail(), want) {
			t.Errorf("preflight detail lacks %q: %s", want, refused.Detail())
		}
	}
	for _, readable := range []string{"- OWN", "- SHARED", "- MAYBE"} {
		if strings.Contains(refused.Detail(), readable) {
			t.Errorf("preflight names readable or optional secret %s", readable)
		}
	}
	if _, err := tenant.AcceptPlan(t.Context(), request); !errors.As(err, &refused) {
		t.Fatalf("repeated preflight: %v", err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), request.RunID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Kind != eventKindSecretPreflightRefused {
		t.Fatalf("preflight diagnostic did not persist once: %+v, %v", events, err)
	}
	if !events[0].At.Equal(*now) {
		t.Fatalf("preflight used another clock: %s, want %s", events[0].At, *now)
	}
	var diagnostic struct{ Message string }
	if err := json.Unmarshal(events[0].Payload, &diagnostic); err != nil || diagnostic.Message != refused.Detail() {
		t.Fatalf("preflight detail: %q, %v", diagnostic.Message, err)
	}
	run, err := tenant.ReadRun(t.Context(), request.RunID)
	if err != nil || run.Spec != nil {
		t.Fatalf("preflight persisted a seal: %+v, %v", run.Spec, err)
	}
	rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
	request.Seal.Resolved.SecretNames = []string{"OWN", "SHARED"}
	if _, err := tenant.AcceptPlan(t.Context(), request); err != nil {
		t.Fatalf("readable secrets or absent optional secret refused: %v", err)
	}
}

func TestAcceptedPlanUsesSharedStrictValidator(t *testing.T) {
	for _, body := range []string{
		`{"nodes":[]}`,
		`{"jobs":[]}{"jobs":[]}`,
		`{"jobs":[{"id":"job","id":"other"}]}`,
		`{"jobs":[{"id":"job","unknown":true}]}`,
		`{"jobs":[{"id":"job","deps":["missing"]}]}`,
		`{"jobs":[{"id":"a","deps":["b"]},{"id":"b","deps":["a"]}]}`,
		`{"jobs":[{"id":"job","resources":{"cores":0}}]}`,
		`{"jobs":[],"source":{"depth":-1}}`,
		`{"jobs":[{"id":"job","children":[{"pipeline":"deploy","repository":"github.com/acme/repo"}]}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			tenant, request := sharedSettingsPlan(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}})
			request.Seal.Plan = []byte(body)
			if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
				t.Fatal("invalid plan accepted")
			}
			run, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || run.Spec != nil {
				t.Fatalf("invalid plan persisted a seal: %+v, %v", run.Spec, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
		})
	}
}

func TestAcceptedPlanIdentifierSecretsProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(81, 2009))
	for range 32 {
		secret := fmt.Sprintf("%s-%x", []string{"s", "\"", "\\", "\n", "é", "😀", ":"}[random.IntN(7)], random.Uint64())
		if random.IntN(4) == 0 {
			secret = "xxxx"
		}
		tenant, request := sharedSettingsPlan(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job"}}})
		request.Seal.Resolved.Args["secret"] = secret
		request.Seal.Resolved.ArgumentDeclarations = []nodeproto.ArgumentDescription{{Name: "secret", Type: "string", Secret: true, Default: &secret}}
		request.Seal.Resolved.SecretArgs = []string{"message", "secret"}
		for _, job := range []nodeproto.JobSpec{
			{ID: "job-" + secret, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			{ID: "job", Groups: []string{"group-" + secret}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			{ID: "job", Steps: []nodeproto.StepSpec{{ID: "step-" + secret}}},
		} {
			body, err := json.Marshal(nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{job}})
			if err != nil {
				t.Fatal(err)
			}
			request.Seal.Plan = body
			_, err = tenant.AcceptPlan(t.Context(), request)
			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != apierr.SecretInIdentifier || strings.Contains(err.Error(), secret) {
				t.Fatalf("secret-bearing identifier accepted or disclosed: %v", err)
			}
			diagnostic, err := json.Marshal(refusal)
			if err != nil || strings.Contains(string(diagnostic), secret) {
				t.Fatalf("secret-bearing diagnostic: %s, %v", diagnostic, err)
			}
			rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
			run, err := tenant.ReadRun(t.Context(), request.RunID)
			if err != nil || run.Spec != nil {
				t.Fatalf("secret-bearing plan persisted: %v", err)
			}
		}
	}
}

func TestPreflightDiagnosticCannotOutlivePlanOwnerProperty(t *testing.T) {
	for remaining := 1; remaining <= 64; remaining++ {
		tenant, request, now := planFixture(t, runspec.DispatchHost)
		request.Seal.Resolved.SecretNames = []string{"ABSENT"}
		deadline := now.Add(time.Duration(remaining) * time.Nanosecond)
		rewriteSchemaExec(t, tenant.s.db, `UPDATE triggers SET lease_expires_at=?`, deadline.UnixNano())
		tenant.s.clock = func() time.Time {
			*now = now.Add(time.Nanosecond)
			return *now
		}
		if _, err := tenant.AcceptPlan(t.Context(), request); err == nil {
			t.Fatal("missing secret accepted")
		}
		events, err := tenant.ReadStateEvents(t.Context(), request.RunID, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Kind != eventKindSecretPreflightRefused || !event.At.Before(deadline) || !now.Before(deadline) {
				t.Fatalf("expired owner wrote a diagnostic: at=%s deadline=%s now=%s", event.At, deadline, *now)
			}
		}
		rewriteSchemaCount(t, tenant.s.db, "nodes", 0)
	}
}
