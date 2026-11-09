package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

const (
	hereSharedValue   = "here-shared-team-value"
	herePipelineValue = "here-pipeline-only-value"
	hereReservedValue = "here-reserved-pool-value"
	hereUndeclared    = "here-undeclared-shared-value"
)

type hereSecretRun struct {
	s       *serverFixture
	options client.EngineOptions
	input   runspec.Request
	runID   string
	award   string
}

func hereSecretFixture(t *testing.T, declared, optional []string) (hereSecretRun, error) {
	t.Helper()
	s, options, input, _ := sourceUploadFixture(t)
	s.WithEngineController(NewEngineController(s.store, EngineController{RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })}))
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	repo := string(input.Git.Repository.Key)
	for _, row := range []store.Secret{
		{Name: "TOKEN", Value: hereSharedValue, Masked: true, Shared: true, Principal: "admin"},
		{Name: "TOKEN", Value: herePipelineValue, Repo: repo, Pipeline: input.Pipeline, Masked: true, Principal: "admin"},
		{Name: "PIPELINE_ONLY", Value: herePipelineValue, Repo: repo, Pipeline: input.Pipeline, Masked: true, Principal: "admin"},
		{Name: "RESERVED", Value: hereReservedValue, Masked: true, Shared: true, Principal: "admin"},
		{Name: "UNDECLARED", Value: hereUndeclared, Masked: true, Shared: true, Principal: "admin"},
	} {
		sealed, err := store.SealSecret(s.cipher, "a", row)
		if err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedSecret(s.store, "a", sealed.Row(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	engineSQL(t, s.store, `UPDATE secrets SET only_on='prod' WHERE team='a' AND name='RESERVED'`)
	input.Reason = "hotfix: production checkout is down"
	input.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "here-secret"})
	if err != nil {
		t.Fatal(err)
	}
	here := hereSecretRun{s: s, options: options, input: input, runID: created.RunID}
	here.award = hereClaim(t, options, created.HereBearer, created.RunID)
	if here.award == "" {
		t.Fatal("the run's own here bearer could not claim its trigger")
	}
	plan := nodewire.PlanDoc{Jobs: []nodewire.JobSpec{{ID: "job", Steps: []nodewire.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	stored := storedIntake(t, s.store, created.RunID)
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: stored, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	owner := options
	owner.Bearer = func(context.Context) (string, error) { return here.award, nil }
	accepting, err := client.NewEngineClient(owner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = accepting.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Env: input.Env, Args: map[string]string{}, SecretArgs: []string{}, SecretNames: append(append([]string{}, declared...), optional...), OptionalSecretNames: optional, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	return here, err
}

func hereClaim(t *testing.T, options client.EngineOptions, bearer, run string) string {
	t.Helper()
	options.Bearer = func(context.Context) (string, error) { return bearer, nil }
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1", RunID: run, Kinds: []capability.UnitKind{capability.UnitTrigger}})
	if err != nil || response.Claim == nil {
		return ""
	}
	return response.Claim.Capability
}

func (h hereSecretRun) release(bearer, name, run string) (int, string) {
	reply := sourceUploadHTTP(h.s.Handler(), bearer, "GET", "/api/v1/secrets/"+name+"?run="+run, "")
	return reply.Code, reply.Body.String()
}

func (h hereSecretRun) releaseAudits(t *testing.T) int {
	t.Helper()
	var audits int
	if err := teststore.SQL(t, h.s.store).QueryRow(`SELECT COUNT(*) FROM audit_events WHERE team='a' AND run_id=? AND kind='secret.released'`, h.runID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	return audits
}

func (h hereSecretRun) refuse(t *testing.T, what, bearer, name string) string {
	t.Helper()
	before := h.releaseAudits(t)
	code, body := h.release(bearer, name, h.runID)
	if code == http.StatusOK {
		t.Fatalf("%s: released HTTP %d %s", what, code, body)
	}
	for _, value := range []string{hereSharedValue, herePipelineValue, hereReservedValue, hereUndeclared} {
		if strings.Contains(body, value) {
			t.Fatalf("%s: refusal exposed a secret value", what)
		}
	}
	if after := h.releaseAudits(t); after != before {
		t.Fatalf("%s: refusal recorded a release audit (%d -> %d)", what, before, after)
	}
	return body
}

func secondMember(t *testing.T, h hereSecretRun) string {
	t.Helper()
	raw := "intruder-account-session"
	digest := sha256.Sum256([]byte(raw))
	now := time.Now().UTC()
	engineSQL(t, h.s.store, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('intruder','intruder@example.test',1,1)`)
	engineSQL(t, h.s.store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','intruder','editor',1)`)
	engineSQL(t, h.s.store, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,'intruder','runs.read,runs.write',?,?,'a','intruder')`, hex.EncodeToString(digest[:]), now.UnixNano(), now.Add(time.Hour).UnixNano())
	engineSQL(t, h.s.store, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,'a',0)`, hex.EncodeToString(digest[:]))
	w := credentialRequestHTTP(t, h.s.Handler(), raw, `{"grants":["runs.write","runs.read","runs.control"]}`, false)
	var credential credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &credential) != nil {
		t.Fatalf("intruder credential: %d %s", w.Code, w.Body)
	}
	return credential.Capability
}

// A here run's shared secrets reach only its own live claim; nobody else gets a path to them.
func TestHereRunReleasesOnlySharedSecretsToItsOwnLiveClaim(t *testing.T) {
	t.Run("own claim reads the shared value, never the pipeline copy", func(t *testing.T) {
		h, err := hereSecretFixture(t, []string{"TOKEN"}, []string{"PIPELINE_ONLY"})
		if err != nil {
			t.Fatal(err)
		}
		code, body := h.release(h.award, "TOKEN", h.runID)
		var secret controllerwire.SecretValue
		if code != http.StatusOK || json.Unmarshal([]byte(body), &secret) != nil || secret.Value != hereSharedValue || !secret.Masked {
			t.Fatalf("own release: HTTP %d", code)
		}
		h.refuse(t, "pipeline-only secret", h.award, "PIPELINE_ONLY")
		if storedIntake(t, h.s.store, h.runID).Request.Reason != h.input.Reason {
			t.Fatal("the here run did not record its reason")
		}
		if audits := h.releaseAudits(t); audits != 1 {
			t.Fatalf("release audits=%d", audits)
		}
	})
	t.Run("reserved-pool secret refused at plan acceptance", func(t *testing.T) {
		if _, err := hereSecretFixture(t, []string{"RESERVED"}, nil); err == nil || !strings.Contains(err.Error(), "restricted secret requires an authorized pool") || strings.Contains(err.Error(), hereReservedValue) {
			t.Fatalf("here plan declaring a reserved-pool secret: %v", err)
		}
	})
	t.Run("undeclared secret", func(t *testing.T) {
		h, err := hereSecretFixture(t, []string{"TOKEN"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if body := h.refuse(t, "undeclared shared secret", h.award, "UNDECLARED"); !strings.Contains(body, string(apierr.ReasonSecretUndeclared)) {
			t.Fatalf("undeclared refusal did not name the missing declaration: %s", body)
		}
	})
	t.Run("submitting member's own credential is not a claim", func(t *testing.T) {
		h, err := hereSecretFixture(t, []string{"TOKEN"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		member, err := h.options.Bearer(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		h.refuse(t, "member credential", member, "TOKEN")
	})
	t.Run("another member", func(t *testing.T) {
		h, err := hereSecretFixture(t, []string{"TOKEN"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		intruder := secondMember(t, h)
		h.refuse(t, "other member credential", intruder, "TOKEN")
		if award := hereClaim(t, h.options, intruder, h.runID); award != "" {
			t.Fatal("another member claimed the here run's trigger")
		}
		options := h.options
		options.Bearer = func(context.Context) (string, error) { return intruder, nil }
		c, err := client.NewEngineClient(options)
		if err != nil {
			t.Fatal(err)
		}
		other, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: h.input, IdempotencyKey: "intruder-here"})
		if err != nil {
			t.Fatal(err)
		}
		if award := hereClaim(t, h.options, other.HereBearer, h.runID); award != "" {
			t.Fatal("another run's here bearer claimed this run")
		}
		otherAward := hereClaim(t, h.options, other.HereBearer, other.RunID)
		if otherAward == "" {
			t.Fatal("the other member's here run could not claim itself")
		}
		h.refuse(t, "other run's award", otherAward, "TOKEN")
	})
	for _, attack := range []struct {
		name  string
		apply string
	}{
		{"replayed after reclaim", `UPDATE triggers SET generation=generation+1 WHERE team='a' AND id=?`},
		{"expired claim", `UPDATE triggers SET lease_expires_at=1 WHERE team='a' AND id=?`},
		{"run deadline passed", `UPDATE triggers SET expires_at=1 WHERE team='a' AND id=?`},
		{"cancelled run", `UPDATE runs SET cancel_requested_at=1 WHERE team='a' AND id=?`},
		{"finished run", `UPDATE runs SET status='failed',finished_at=1 WHERE team='a' AND id=?`},
		{"member removed", `DELETE FROM memberships WHERE team='a' AND account_id='member' AND ? <> ''`},
	} {
		t.Run(attack.name, func(t *testing.T) {
			h, err := hereSecretFixture(t, []string{"TOKEN"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			engineSQL(t, h.s.store, attack.apply, h.runID)
			h.refuse(t, attack.name, h.award, "TOKEN")
		})
	}
}
