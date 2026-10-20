package controller_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func setComputeLimit(t *testing.T, f creditsFixture, name string, value int64) {
	t.Helper()
	status, body := f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		map[string]any{"limits": map[string]int64{name: value}})
	if status != http.StatusOK {
		t.Fatalf("set %s: status = %d: %s", name, status, body)
	}
}

func computeIntake(pipeline string) controllerwire.CreateRunRequest {
	return controllerwire.CreateRunRequest{Request: runspec.Request{Version: 1, Pipeline: pipeline, Mode: runspec.IntakeExecute, Dispatch: runspec.DispatchHost, Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, SHA: fortyHex(1)}, Source: runspec.Source{Ref: fortyHex(1)}}}
}

func TestComputeLimits_ShowAndSet(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t)

	status, body := f.request(t, http.MethodGet, "/api/v1/operator/limits", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("show: status = %d: %s", status, body)
	}
	var view struct {
		Limits map[string]int64 `json:"limits"`
		Usage  struct {
			Runners      int64 `json:"runners"`
			AlarmReached bool  `json:"alarm_reached"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, name := range store.ComputeLimitNames() {
		if view.Limits[name] != 0 {
			t.Fatalf("guard %s = %d on a fresh controller, want 0", name, view.Limits[name])
		}
	}
	if view.Usage.Runners != 0 {
		t.Fatalf("runners = %d, want 0", view.Usage.Runners)
	}

	setComputeLimit(t, f, store.ComputeLimitGlobalRunners, 12)
	status, body = f.request(t, http.MethodGet, "/api/v1/operator/limits", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("show after set: status = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Limits[store.ComputeLimitGlobalRunners] != 12 {
		t.Fatalf("global runners = %d, want 12", view.Limits[store.ComputeLimitGlobalRunners])
	}

	if status, _ := f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		map[string]any{"limits": map[string]int64{"max_dollars": 1}}); status != http.StatusBadRequest {
		t.Fatalf("an unknown guard = %d, want 400", status)
	}
	if status, _ := f.request(t, http.MethodPut, "/api/v1/operator/limits", f.readonly,
		map[string]any{"limits": map[string]int64{store.ComputeLimitGlobalRunners: 1}}); status != http.StatusForbidden {
		t.Fatalf("a reader setting a guard = %d, want 403", status)
	}
}

func TestComputeLimits_RefusesBadSiblingWithoutChangingGuards(t *testing.T) {
	f := newCreditsFixture(t)
	for range 20 {
		status, body := f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
			map[string]any{"limits": map[string]int64{
				store.ComputeLimitGlobalRunners: 7,
				store.ComputeLimitRunSeconds:    -1,
			}})
		if status != http.StatusBadRequest {
			t.Fatalf("mixed write = %d: %s", status, body)
		}
	}
	status, body := f.request(t, http.MethodGet, "/api/v1/operator/limits", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("show = %d: %s", status, body)
	}
	var view struct {
		Limits map[string]int64 `json:"limits"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := view.Limits[store.ComputeLimitGlobalRunners]; got != 0 {
		t.Fatalf("the good half of a refused write set the global runner cap to %d", got)
	}
}

func TestComputeLimits_RefusesExplicitNullWithoutChangingGuards(t *testing.T) {
	f := newCreditsFixture(t)
	setComputeLimit(t, f, store.ComputeLimitGlobalRunners, 7)

	status, body := f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		map[string]any{"limits": map[string]any{store.ComputeLimitGlobalRunners: nil}})
	if status != http.StatusBadRequest {
		t.Fatalf("null write = %d: %s", status, body)
	}
	status, body = f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		map[string]any{"limits": nil})
	if status != http.StatusBadRequest {
		t.Fatalf("null limits = %d: %s", status, body)
	}
	status, body = f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		map[string]any{"LIMITS": map[string]any{store.ComputeLimitGlobalRunners: nil}})
	if status != http.StatusBadRequest {
		t.Fatalf("case-folded null write = %d: %s", status, body)
	}
	status, body = f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		json.RawMessage(`{"limits":{"max_global_runners":null},"limits":{"runner_alarm":3}}`))
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate null write = %d: %s", status, body)
	}
	status, body = f.request(t, http.MethodGet, "/api/v1/operator/limits", f.admin, nil)
	if status != http.StatusOK {
		t.Fatalf("show = %d: %s", status, body)
	}
	var view struct {
		Limits map[string]int64 `json:"limits"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := view.Limits[store.ComputeLimitGlobalRunners]; got != 7 {
		t.Fatalf("explicit null reset the global runner cap to %d", got)
	}
	status, body = f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		map[string]any{"LIMITS": map[string]any{store.ComputeLimitGlobalRunners: 8}})
	if status != http.StatusOK {
		t.Fatalf("case-folded valid write = %d: %s", status, body)
	}
	status, body = f.request(t, http.MethodPut, "/api/v1/operator/limits", f.admin,
		json.RawMessage(`{"limits":{"max_global_runners":9},"LIMITS":{"runner_alarm":3}}`))
	if status != http.StatusOK {
		t.Fatalf("duplicate valid write = %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode duplicate result: %v", err)
	}
	if view.Limits[store.ComputeLimitGlobalRunners] != 9 ||
		view.Limits[store.ComputeLimitRunnerAlarm] != 3 {
		t.Fatalf("duplicate valid write did not merge: %v", view.Limits)
	}
}

func TestComputeLimits_RunCreationRefusedPastTheHourlyCap(t *testing.T) {
	f := newCreditsFixture(t)
	f.store.SetIntakePolicy(receiptPolicy{})
	setComputeLimit(t, f, store.ComputeLimitGlobalRunsPerHour, 1)
	seedRunNode(t, f.store, "run-first", "build")

	status, body := f.request(t, http.MethodPost, "/api/v1/runs", f.writer, computeIntake("demo"))
	if status != http.StatusTooManyRequests {
		t.Fatalf("the run past the cap = %d, want 429: %s", status, body)
	}
}

func TestComputeLimits_CronBelowTheMinimumIntervalIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCronsFixture(t)
	admin, _, err := teststore.ProvisionCredential(context.Background(), f.store, capability.ProvisionSpec{Kind: capability.ProvisionBootstrap, Team: "", Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "root"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}, ExpiresAt: teststore.CredentialExpiry(time.Now().UTC(), 0)})
	if err != nil {
		t.Fatalf("admin token: %v", err)
	}
	f.call(http.MethodPut, "/api/v1/operator/limits", admin,
		map[string]any{"limits": map[string]int64{store.ComputeLimitCronSeconds: 900}},
		http.StatusOK, nil)

	body := map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "sweep", "cron": "*/5 * * * *"},
		},
	}
	f.call(http.MethodPut, "/api/v1/schedules/repos", f.writer, body, http.StatusTooManyRequests, nil)

	body["schedules"] = []map[string]any{{"pipeline": "sweep", "cron": "0 * * * *"}}
	f.call(http.MethodPut, "/api/v1/schedules/repos", f.writer, body, http.StatusOK, nil)
}

// safety: a trigger creates the run it names, so the hourly guard has to see
// the triggering principal rather than an unowned run.
func TestComputeLimits_TriggeredRunsCountAgainstTheTriggeringPrincipal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	f := newCreditsFixture(t)
	ctx := context.Background()
	setComputeLimit(t, f, store.ComputeLimitRunsPerHour, 1)
	writer, _, err := teststore.ProvisionCredential(ctx, f.store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "pool"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.write", "runs.control", "runs.read"}), ExpiresAt: teststore.CredentialExpiry(time.Now().UTC(), 0)})
	if err != nil {
		t.Fatalf("mint a writer for the metered principal: %v", err)
	}

	f.store.SetIntakePolicy(receiptPolicy{})
	body := computeIntake("demo")
	status, first := f.request(t, http.MethodPost, "/api/v1/runs", writer, body)
	if status != http.StatusCreated {
		t.Fatalf("first intake = %d: %s", status, first)
	}
	status, second := f.request(t, http.MethodPost, "/api/v1/runs", writer, body)
	if status != http.StatusTooManyRequests {
		t.Fatalf("second intake = %d, want 429: %s", status, second)
	}

	runs, err := f.store.AsOperator().ListRuns(ctx, store.RunFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want the refused one absent", len(runs))
	}
}

// safety: a schedule's runs belong to the principal that armed it, which is
// what makes a hot cron count against that team rather than nobody.
func TestComputeLimits_CronLaunchesCarryTheArmingPrincipal(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCronsFixture(t)
	ctx := context.Background()
	body := map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "nightly", "cron": "0 3 * * *"},
		},
	}
	f.call(http.MethodPut, "/api/v1/schedules/repos", f.writer, body, http.StatusOK, nil)

	rows, err := teststore.ForTeam(t, f.store, store.DefaultTeam).ListCronSchedules(ctx)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("schedules = %d, want one", len(rows))
	}
	if rows[0].ArmedBy != "pusher" {
		t.Fatalf("armed_by = %q, want the arming principal", rows[0].ArmedBy)
	}
}

// safety: run-now is a launch like any other, so a guard has to refuse it with
// the same code every other surface answers.
func TestComputeLimits_CronRunNowAtTheCapAnswers429(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.2s of real work; the fast class runs under -short")
	}
	f := newCronsFixture(t)
	ctx := context.Background()
	body := map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "nightly", "cron": "0 3 * * *"},
		},
	}
	f.call(http.MethodPut, "/api/v1/schedules/repos", f.writer, body, http.StatusOK, nil)
	rows, err := teststore.ForTeam(t, f.store, store.DefaultTeam).ListCronSchedules(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list schedules = %d rows, %v", len(rows), err)
	}
	if err := teststore.SeedRun(f.store, ctx, store.DefaultTeam, store.Run{
		ID: "run-already", Pipeline: "demo", Status: "running", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed a run: %v", err)
	}
	if _, err := f.store.AsOperator().SetComputeLimits(ctx, map[string]int64{store.ComputeLimitGlobalRunsPerHour: 1}); err != nil {
		t.Fatalf("set the guard: %v", err)
	}

	var refusal struct {
		Error struct {
			Code    apierr.Code `json:"code"`
			Details struct {
				Limit string `json:"limit"`
			} `json:"details"`
		} `json:"error"`
	}
	f.call(http.MethodPost, "/api/v1/schedules/"+rows[0].ID+"/run", f.writer, nil,
		http.StatusTooManyRequests, &refusal)
	if refusal.Error.Code != apierr.RateLimited ||
		refusal.Error.Details.Limit != store.ComputeLimitGlobalRunsPerHour {
		t.Fatalf("refusal = %+v, want the hourly guard named", refusal)
	}
	triggers, err := teststore.ForTeam(t, f.store, store.DefaultTeam).ListTriggers(ctx, store.TriggerFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list triggers: %v", err)
	}
	if len(triggers) != 0 {
		t.Fatalf("the refused run-now left %d triggers behind", len(triggers))
	}
}
