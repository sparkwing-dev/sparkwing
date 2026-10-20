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
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
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
	s.WithEngineController(&EngineController{Store: s.store, RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })})
	options.HTTP = &http.Client{Transport: inventoryRoundTrip{s.Handler()}}
	input.LocalSecretNames = []string{"TOKEN"}
	input.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	input.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}, {Target: "machines"}, {Target: "cloud"}}}
	submittedValue := "submitted-environment-" + value
	environment, err := runspec.EnvironmentDigest(map[string]string{"BUILD_REGION": submittedValue, "BUILD_EMPTY": ""}, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	input.Env = environment
	input.Options.SubmitEnvAllow = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"BUILD_*"}}}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	input.EnvironmentSource = runspec.EnvironmentSubmitted
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "local-secret", LocalSecrets: map[string]string{"TOKEN": value}, SubmittedEnv: map[string][]byte{"BUILD_REGION": []byte(submittedValue), "BUILD_EMPTY": {}}})
	if err != nil {
		t.Fatal(err)
	}
	options.Bearer = func(context.Context) (string, error) { return created.HereBearer, nil }
	here, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	response, err := here.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: "v1", RunID: created.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 1})
	if err != nil || response.Claim == nil {
		t.Fatalf("here claim: %+v %v", response, err)
	}
	award := response.Claim
	if len(award.SubmittedEnv) != 2 || string(award.SubmittedEnv["BUILD_REGION"]) != submittedValue {
		t.Fatal("private direct award lost submitted environment")
	}
	masker := secrets.NewMasker()
	base, err := procenv.WithSubmitted(procenv.Base{Ambient: []string{"UNLISTED=runner-private"}, Masker: masker}, award.SubmittedEnv, procenv.SubmitAcceptAll())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []procenv.Kind{procenv.Install, procenv.Plan, procenv.Attempt, procenv.Eval} {
		environment, err := procenv.Build(kind, base, procenv.Runtime{})
		if err != nil || strings.Contains(strings.Join(environment, "\n"), "BUILD_REGION="+submittedValue) == (kind == procenv.Install) || strings.Contains(strings.Join(environment, "\n"), "UNLISTED=") {
			t.Fatal("remote process lost submitted values or inherited unlisted input", err)
		}
	}
	if masker.ContainsSecret(submittedValue) == false {
		t.Fatal("submitted values are not masked")
	}
	options.Bearer = func(context.Context) (string, error) { return award.Capability, nil }
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
		_, err = owner.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: badBody, Resolved: runspec.Resolved{Env: created.Request.Request.Env, Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"TOKEN"}, Settings: badResolved, Guards: runspec.Guards{Passed: true}}})
		if err == nil || value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("local secret reached a persisted plan identifier or its refusal")
		}
	}
	accepted, err := owner.AcceptPlan(t.Context(), created.RunID, controllerwire.PlanSubmission{Declarations: declarations, Plan: body, Resolved: runspec.Resolved{Env: created.Request.Request.Env, Args: map[string]string{}, SecretArgs: []string{}, SecretNames: []string{"TOKEN"}, Settings: resolved, Guards: runspec.Guards{Passed: true}}})
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
	reply := sourceUploadHTTP(h, award.Capability, "GET", path, "")
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
	if string(reconciled.Claim.SubmittedEnv["BUILD_REGION"]) != submittedValue {
		t.Fatal("node award lost submitted environment")
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
	if err := teststore.SQL(t, s.store).QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &database, &databasePath); err != nil || databasePath == "" {
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
	other, err := teststore.OpenExisting(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherServer := New(other, nil).WithSecretsCipher(s.secretsCipher).WithEngineController(&EngineController{Store: other})
	otherTenant, err := other.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	retained, err := otherTenant.ReadSubmittedEnvironment(t.Context(), principal.Authority)
	if err != nil || string(retained["BUILD_REGION"]) != submittedValue {
		t.Fatal("submitted environment did not survive another controller process", err)
	}
	lost := sourceUploadHTTP(otherServer.Handler(), reconciled.Claim.Capability, "GET", "/api/v1/secrets/TOKEN?run="+created.RunID, "")
	if lost.Code != http.StatusServiceUnavailable || value != "" && strings.Contains(lost.Body.String(), value) {
		t.Fatalf("another process restored or exposed requester values: HTTP %d", lost.Code)
	}
	resupply, err := json.Marshal(map[string]any{"local_secrets": map[string]string{"TOKEN": value}})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := "/api/v1/claims/" + award.UnitID + "/heartbeat"
	rejected := sourceUploadHTTP(otherServer.Handler(), reconciled.Claim.Capability, "POST", "/api/v1/claims/"+reconciled.Claim.UnitID+"/heartbeat", string(resupply))
	if rejected.Code == http.StatusOK {
		t.Fatal("worker replaced requester secrets")
	}
	renewed := sourceUploadHTTP(otherServer.Handler(), award.Capability, "POST", heartbeat, string(resupply))
	if renewed.Code != http.StatusOK {
		t.Fatalf("here resupply after controller restart: HTTP %d %s", renewed.Code, renewed.Body)
	}
	restored := sourceUploadHTTP(otherServer.Handler(), reconciled.Claim.Capability, "GET", path, "")
	var restoredValue controllerwire.SecretValue
	if restored.Code != http.StatusOK || json.Unmarshal(restored.Body.Bytes(), &restoredValue) != nil || restoredValue.Value != value {
		t.Fatalf("resumed burst lost requester secret: HTTP %d", restored.Code)
	}
	for _, invalid := range []map[string]string{{"TOKEN": "changed-value"}, {"OTHER": "value"}, {"TOKEN": value, "OTHER": "value"}} {
		body, err := json.Marshal(map[string]any{"local_secrets": invalid})
		if err != nil {
			t.Fatal(err)
		}
		rejected := sourceUploadHTTP(otherServer.Handler(), award.Capability, "POST", heartbeat, string(body))
		if rejected.Code == http.StatusOK {
			t.Fatal("here renewal replaced sealed requester inputs")
		}
	}
	if live := sourceUploadHTTP(h, reconciled.Claim.Capability, "GET", "/api/v1/secrets/TOKEN?run="+created.RunID, ""); live.Code != http.StatusOK {
		t.Fatalf("another process changed the original process's secret delivery: HTTP %d", live.Code)
	}
	for _, name := range []string{"TOKEN?run=foreign", "UNDECLARED?run=" + created.RunID} {
		refused := sourceUploadHTTP(h, award.Capability, "GET", "/api/v1/secrets/"+name, "")
		if refused.Code == http.StatusOK || value != "" && strings.Contains(refused.Body.String(), value) {
			t.Fatalf("foreign release: %d %s", refused.Code, refused.Body)
		}
	}
	engineSQL(t, s.store, `UPDATE triggers SET generation=generation+1 WHERE team='a' AND id=?`, created.RunID)
	refused := sourceUploadHTTP(h, award.Capability, "GET", path, "")
	if refused.Code == http.StatusOK || value != "" && strings.Contains(refused.Body.String(), value) {
		t.Fatalf("stale release: %d %s", refused.Code, refused.Body)
	}
}

func TestHTTPIntakeReplayValidatesPrivateInputs(t *testing.T) {
	httpIntakeReplayPrivateInputs(t, "original-secret")
}

func FuzzHTTPIntakeReplayValidatesPrivateInputs(f *testing.F) {
	f.Add([]byte("signing-material"))
	f.Add([]byte{0, 255, 34, 10})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			t.Skip()
		}
		httpIntakeReplayPrivateInputs(t, string(data))
	})
}

func httpIntakeReplayPrivateInputs(t *testing.T, value string) {
	t.Helper()
	s, _, input, bearer := sourceUploadFixture(t)
	s.WithEngineController(&EngineController{Store: s.store, RunnerPolicy: runnerAdmissionFunc(func(context.Context, string) error { return nil })})
	input.LocalSecretNames = []string{"SIGNING_MATERIAL"}
	input.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}}
	input.Burst = &runspec.BurstPlacement{Preferences: []runspec.PlacementStep{{Target: "here"}}}
	input.EnvironmentSource = runspec.EnvironmentSubmitted
	input.Options.SubmitEnvAllow = runspec.Setting[[]string]{Optional: runspec.Optional[[]string]{Set: true, Value: []string{"BUILD_REGION"}}}
	var err error
	input.Env, err = runspec.EnvironmentDigest(map[string]string{"BUILD_REGION": "original"}, strings.Repeat("5", 64))
	if err != nil {
		t.Fatal(err)
	}
	request := controllerwire.CreateRunRequest{Request: input, IdempotencyKey: "private-replay", LocalSecrets: map[string]string{"SIGNING_MATERIAL": value}, SubmittedEnv: map[string][]byte{"BUILD_REGION": []byte("original")}}
	send := func(handler http.Handler, req controllerwire.CreateRunRequest, want int) {
		t.Helper()
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		w := sourceUploadHTTP(handler, bearer, "POST", "/api/v1/runs", string(body))
		if w.Code != want {
			t.Fatalf("intake HTTP %d, want %d: %s", w.Code, want, w.Body)
		}
	}
	send(s.Handler(), request, http.StatusCreated)
	send(s.Handler(), request, http.StatusOK)
	changed := request
	changed.LocalSecrets = map[string]string{"SIGNING_MATERIAL": value + "changed"}
	send(s.Handler(), changed, http.StatusConflict)
	changed.LocalSecrets = nil
	send(s.Handler(), changed, http.StatusBadRequest)
	changed = request
	changed.SubmittedEnv = map[string][]byte{"BUILD_REGION": []byte("changed")}
	send(s.Handler(), changed, http.StatusConflict)
	changed.SubmittedEnv = nil
	send(s.Handler(), changed, http.StatusConflict)
	var sequence int
	var database, path string
	if err := teststore.SQL(t, s.store).QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &database, &path); err != nil {
		t.Fatal(err)
	}
	other, err := teststore.OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	server := New(other, nil).WithSecretsCipher(s.secretsCipher).WithEngineController(&EngineController{Store: other})
	send(server.Handler(), request, http.StatusServiceUnavailable)
	send(server.Handler(), request, http.StatusServiceUnavailable)
}
