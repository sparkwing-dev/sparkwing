package controller

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRequesterLocalSecretRealIntakeClaimAndRelease(t *testing.T) {
	for _, value := range []string{"requester-local-secret-value", ""} {
		t.Run(fmt.Sprintf("empty=%v", value == ""), func(t *testing.T) { requesterLocalSecretRelease(t, value) })
	}
}

func FuzzRequesterLocalSecretNeverPersistsOrFallsBackOnAnotherProcess(f *testing.F) {
	f.Add([]byte("private-value"))
	f.Add([]byte{0, 255, 34, 10})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			return
		}
		requesterLocalSecretRelease(t, "requester-only-"+hex.EncodeToString(data))
	})
}

func requesterLocalSecretRelease(t *testing.T, value string) {
	t.Helper()
	s, options, input, _ := sourceUploadFixture(t)
	input.LocalSecretNames = []string{"TOKEN"}
	input.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "machines"}, {Target: "cloud"}}}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "local-secret", LocalSecrets: map[string]string{"TOKEN": value}})
	if err != nil {
		t.Fatal(err)
	}
	engineHTTPRoot(t, &EngineController{Store: s.store}, "a", "secret-runner", []capability.Grant{capability.TriggersClaim})
	s.store.SetClaimProfileResolver(engineProfileFake{})
	award, err := s.store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "secret-runner", Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: created.RunID}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil || len(award.Bearer) == 0 {
		t.Fatalf("claim: %v", err)
	}
	options.Bearer = func(context.Context) (string, error) { return string(award.Bearer), nil }
	owner, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	plan := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Steps: []nodeproto.StepSpec{{ID: "run"}}}}}
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	declarations := runspec.CheckoutDeclarations{Version: 1}
	resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: created.Request, Declarations: declarations, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if value != "" {
		badPlan := plan
		badPlan.Jobs = append([]nodeproto.JobSpec{}, plan.Jobs...)
		badPlan.Jobs[0].ID = value
		badBody, err := json.Marshal(badPlan)
		if err != nil {
			t.Fatal(err)
		}
		badResolved, err := settings.ResolvePlan(settings.PlanInputs{Request: created.Request, Declarations: declarations, Plan: badPlan})
		if err != nil {
			t.Fatal(err)
		}
		_, err = owner.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: badBody, Resolved: runspec.Resolved{Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"TOKEN"}, Settings: badResolved, Guards: runspec.Guards{Passed: true}}})
		if err == nil || value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("local secret reached a persisted plan identifier or its refusal")
		}
	}
	accepted, err := owner.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"TOKEN"}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Spec.Request.Request.Burst == nil || len(accepted.Spec.Request.Request.Burst.Preferences) != 3 {
		t.Fatal("burst preferences lost from secret-bearing seal")
	}
	if accepted.Spec.Hash == "" {
		t.Fatal("unsealed plan")
	}
	h := s.Handler()
	path := "/api/v1/secrets/TOKEN?run=" + created.RunID
	reply := sourceUploadHTTP(h, string(award.Bearer), "GET", path, "")
	var secret controllerwire.SecretValue
	if reply.Code != http.StatusOK || json.Unmarshal(reply.Body.Bytes(), &secret) != nil || secret.Value != value || !secret.Masked {
		t.Fatalf("release HTTP %d %s", reply.Code, reply.Body)
	}
	if reply.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret release may be cached")
	}
	reconciled, err := owner.ReconcileJob(t.Context(), created.RunID, "job", controllerwire.ReconcileRequest{Policy: "none"})
	if err != nil || reconciled.Claim == nil {
		t.Fatalf("claiming node: %#v %v", reconciled, err)
	}
	nodeReply := sourceUploadHTTP(h, reconciled.Claim.Capability, "GET", path, "")
	var nodeSecret controllerwire.SecretValue
	if nodeReply.Code != http.StatusOK || json.Unmarshal(nodeReply.Body.Bytes(), &nodeSecret) != nil || nodeSecret.Value != value || !nodeSecret.Masked {
		t.Fatalf("node release HTTP %d %s", nodeReply.Code, nodeReply.Body)
	}
	options.Bearer = func(context.Context) (string, error) { return reconciled.Claim.Capability, nil }
	private, err := client.NewNodeHostClient(options, created.RunID, "job")
	if err != nil {
		t.Fatal(err)
	}
	defer private.Close()
	principal, err := private.Principal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	process, err := private.ProcessClient(t.Context(), principal, "job")
	if err != nil {
		t.Fatal(err)
	}
	actual, masked, err := process.Secret(t.Context(), "TOKEN")
	if err != nil || actual != value || !masked {
		t.Fatalf("native secret release: masked=%v error=%v", masked, err)
	}

	tenant, err := s.store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := tenant.ReadRun(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := tenant.ReadStateEvents(t.Context(), created.RunID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := json.Marshal([]any{stored, events})
	if err != nil || value != "" && strings.Contains(string(persisted), value) {
		t.Fatal("plaintext persisted")
	}
	var sequence int
	var database, databasePath string
	if err := s.store.DB().QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &database, &databasePath); err != nil || databasePath == "" {
		t.Fatalf("SQLite database path unavailable: %v", err)
	}
	for _, suffix := range []string{"", "-wal"} {
		body, err := os.ReadFile(databasePath + suffix)
		if suffix != "" && os.IsNotExist(err) {
			continue
		}
		if err != nil || value != "" && bytes.Contains(body, []byte(value)) {
			t.Fatalf("requester secret reached SQLite%s: %v", suffix, err)
		}
	}
	other, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherServer := New(other, nil).WithSecretsCipher(s.secretsCipher)
	lost := sourceUploadHTTP(otherServer.Handler(), reconciled.Claim.Capability, "GET", "/api/v1/secrets/TOKEN?run="+created.RunID, "")
	if lost.Code != http.StatusServiceUnavailable || value != "" && strings.Contains(lost.Body.String(), value) {
		t.Fatalf("another process restored or exposed requester values: HTTP %d", lost.Code)
	}
	if live := sourceUploadHTTP(h, reconciled.Claim.Capability, "GET", "/api/v1/secrets/TOKEN?run="+created.RunID, ""); live.Code != http.StatusOK {
		t.Fatalf("another process changed the original process's secret delivery: HTTP %d", live.Code)
	}
	for _, name := range []string{"TOKEN?run=foreign", "UNDECLARED?run=" + created.RunID} {
		refused := sourceUploadHTTP(h, string(award.Bearer), "GET", "/api/v1/secrets/"+name, "")
		if refused.Code == http.StatusOK || value != "" && strings.Contains(refused.Body.String(), value) {
			t.Fatalf("foreign release: %d %s", refused.Code, refused.Body)
		}
	}
	engineSQL(t, s.store, `UPDATE triggers SET generation=generation+1 WHERE team='a' AND id=?`, created.RunID)
	refused := sourceUploadHTTP(h, string(award.Bearer), "GET", path, "")
	if refused.Code == http.StatusOK || value != "" && strings.Contains(refused.Body.String(), value) {
		t.Fatalf("stale release: %d %s", refused.Code, refused.Body)
	}
}
