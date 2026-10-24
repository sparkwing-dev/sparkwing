package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTrigger_Validation(t *testing.T) {
	f := newBrowserFixture(t, nil)
	for _, body := range []string{`{}`, `{"request":{"team":"default","pipeline":"demo"},"unknown":true}`} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+f.token)
		request.Header.Set("Content-Type", "application/json")
		response := f.serve(request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status=%d want 400: %s", body, response.Code, response.Body)
		}
	}
}

func TestEventListClampsItsLimit(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := t.Context()
	if err := teststore.SeedRun(st, ctx, store.DefaultTeam, store.Run{RunSummary: runstate.RunSummary{ID: "run-1", Pipeline: "push", Status: "running"}}); err != nil {
		t.Fatal(err)
	}
	for range store.MaxRunListLimit + 5 {
		if err := teststore.ForTeam(t, st, store.DefaultTeam).AppendEvent(ctx, "run-1", "node-1", "log", []byte(`{"line":"x"}`)); err != nil {
			t.Fatal(err)
		}
	}
	raw, _, err := teststore.ProvisionCredential(ctx, st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "reader"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead}})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/runs/run-1/events?limit=1000000000", nil)
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	controller.New(st, nil).Handler().ServeHTTP(w, r)
	var events []json.RawMessage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &events) != nil || len(events) != store.MaxRunListLimit {
		t.Fatalf("events HTTP %d count=%d, want %d: %s", w.Code, len(events), store.MaxRunListLimit, w.Body)
	}
}

func TestTrigger_RecordsARunForRunnersToClaim(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	created := floodRun(t, f.submit(f.token, "demo", fortyHex(1)), 201)
	var units int
	if err := teststore.SQL(t, f.st).QueryRow(`SELECT COUNT(*) FROM triggers WHERE team='default' AND id=? AND unit_kind='plan' AND claimed_by IS NULL`, created.RunID).Scan(&units); err != nil || units != 1 {
		t.Fatalf("claimable units=%d: %v", units, err)
	}
}

func TestTrigger_CreatesPendingRunRow(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	created := floodRun(t, f.submit(f.token, "demo", fortyHex(1)), 201)
	tenant := teststore.ForTeam(t, f.st, store.DefaultTeam)
	run, err := tenant.ReadRun(t.Context(), created.RunID)
	if err != nil || run.State != runstate.RunPending || run.Pipeline != "demo" || run.Request.Request.Git.SHA != fortyHex(1) || run.CreatedAt.IsZero() {
		t.Fatalf("run: %+v: %v", run, err)
	}
	rows, err := tenant.ListRuns(t.Context(), store.RunFilter{Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != created.RunID {
		t.Fatalf("list: %+v: %v", rows, err)
	}
}

func TestRunIntakeRefusesClientSuppliedTriggerEnvironment(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	for _, key := range []string{"SPARKWING_LEASE_TOKEN", "SPARKWING_CHILD_LEASE_TOKEN"} {
		raw, err := json.Marshal(computeIntake("demo"))
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		request := body["request"].(map[string]any)
		request["trigger_env"] = map[string]string{key: "stale-private-token", "GITHUB_REPOSITORY": "acme/app"}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(encoded))
		r.Header.Set("Authorization", "Bearer "+f.token)
		r.Header.Set("Content-Type", "application/json")
		w := f.serve(r)
		if w.Code != 400 || strings.Contains(w.Body.String(), "stale-private-token") {
			t.Fatalf("public env: %d %s", w.Code, w.Body)
		}
	}
	var n int
	if err := teststore.SQL(t, f.st).QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused env persisted %d runs: %v", n, err)
	}
}

func TestTrigger_RequiresJSONContentType(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body, err := json.Marshal(computeIntake("demo"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		typ    string
		status int
	}{{"text/plain;charset=UTF-8", 400}, {"application/x-www-form-urlencoded", 400}, {"", 400}, {"application/json", 201}, {"application/json; charset=utf-8", 201}} {
		r := httptest.NewRequest("POST", "/api/v1/runs", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+f.token)
		r.Header.Set("Content-Type", tc.typ)
		w := f.serve(r)
		if w.Code != tc.status {
			t.Fatalf("Content-Type %q: %d %s", tc.typ, w.Code, w.Body)
		}
	}
}

type triggerSourcePolicy struct{ receiptPolicy }

func (p triggerSourcePolicy) ResolveIntake(ctx context.Context, authority capability.AuthorityResolver, request store.IntakeRequest) (store.IntakePolicyFacts, error) {
	facts, err := p.receiptPolicy.ResolveIntake(ctx, authority, request)
	facts.SourceLocation = &request.Request.Source.Location
	return facts, err
}

func TestTrigger_ValidatesRepoURL(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{}, fixtureStorePolicies(store.Policies{Intake: triggerSourcePolicy{}}))
	for _, tc := range []struct {
		remote string
		want   int
	}{
		{"https://example.com/acme/app.git", 201},
		{"git@example.com:acme/app.git", 201},
		{"", 201},
		{"/tmp/repo", 400},
		{"file:///tmp/repo", 400},
		{"https://127.0.0.1/repo.git", 400},
		{"https://user:secret@example.com/repo.git", 400},
	} {
		body := computeIntake("demo")
		body.Request.Source.Location.RemoteURL = tc.remote
		if tc.remote != "" {
			body.Request.Git.Repository = runspec.Repository{}
		}
		w := f.intake(f.token, body)
		if w.Code != tc.want {
			t.Fatalf("remote %q: %d %s", tc.remote, w.Code, w.Body)
		}
	}
}

func TestTrigger_ReservesGitHubProvenanceForTheWebhook(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	property := func(noise string) bool {
		body := computeIntake("demo")
		body.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DeliveryKey: "forged-" + noise, RepositoryID: 701, InstallationID: 7, DefaultBranchPush: true, VerifiedAt: time.Now()}}
		created := floodRun(t, f.intake(f.token, body), 201)
		tenant := teststore.ForTeam(t, f.st, store.DefaultTeam)
		run, err := tenant.ReadRun(t.Context(), created.RunID)
		if err != nil {
			t.Fatal(err)
		}
		return !run.Request.Request.Git.App.Set && !created.Inputs.Git.App.Set
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	body := computeIntake("demo")
	body.Request.Origin = runspec.OriginApp
	if w := f.intake(f.token, body); w.Code != 403 {
		t.Fatalf("forged App origin: %d %s", w.Code, w.Body)
	}
}

func TestTriggerAndCreateRunRejectAGitSHAThatIsNotAnObjectID(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	for _, sha := range []string{"--upload-pack=evil", "main", "abc1234", "HEAD"} {
		if w := f.submit(f.token, "demo", sha); w.Code != 400 {
			t.Fatalf("sha %q: %d %s", sha, w.Code, w.Body)
		}
	}
	floodRun(t, f.submit(f.token, "demo", fortyHex(1)), 201)
}

func TestTrigger_RefusesATriggerThatNamesTwoRepositories(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{}, fixtureStorePolicies(store.Policies{Intake: triggerSourcePolicy{}}))
	property := func(value uint32) bool {
		body := computeIntake("demo")
		body.Request.Source.Location.RemoteURL = "https://example.com/acme/app.git"
		body.Request.Git.Repository.Key = runspec.RepoKey(fmt.Sprintf("git:example.com/evil/repo%d", value))
		w := f.intake(f.token, body)
		return w.Code == 403 && !strings.Contains(w.Body.String(), "private")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	body := computeIntake("demo")
	body.Request.Source.Location.RemoteURL = "git@example.com:acme/app.git"
	body.Request.Git.Repository = runspec.Repository{Key: "git:example.com/acme/app"}
	floodRun(t, f.intake(f.token, body), 201)
}

func TestTriggerRecordsTheRunFlagsOnTheRunRow(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	body := computeIntake("demo")
	body.Request.Options.Only = runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: "ship"}}
	body.Request.Options.DryRun = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}
	body.Request.Options.NoMemo = runspec.Setting[bool]{Optional: runspec.Optional[bool]{Set: true, Value: true}}
	created := floodRun(t, f.intake(f.token, body), 201)
	row, err := teststore.ForTeam(t, f.st, store.DefaultTeam).ReadRun(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got := row.Request.Request.Options
	if !got.Only.Set || got.Only.Value != "ship" || !got.DryRun.Set || !got.DryRun.Value || !got.NoMemo.Set || !got.NoMemo.Value {
		t.Fatalf("flags: %+v", got)
	}
}

func TestRunIntakeRejectsUntrustedRevisionSelectorsProperty(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	property := func(noise string, variant uint8) bool {
		sha := "--upload-pack=" + noise
		switch variant % 3 {
		case 1:
			sha = strings.Repeat("a", 41+int(variant%23))
		case 2:
			sha = strings.Repeat("0", 39) + "g"
		}
		w := f.submit(f.token, "demo", sha)
		return w.Code == 400
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
	floodRun(t, f.submit(f.token, "demo", strings.Repeat("a", 40)), 201)
	floodRun(t, f.submit(f.token, "demo", strings.Repeat("b", 64)), 201)
	var count int
	if err := teststore.SQL(t, f.st).QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("invalid revision persisted runs: %d: %v", count, err)
	}
}

func TestRunIntakeSourceBoundaryProperty(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{}, fixtureStorePolicies(store.Policies{Intake: triggerSourcePolicy{}}))
	property := func(a, b, c uint8) bool {
		body := computeIntake("demo")
		body.Request.Git.Repository = runspec.Repository{}
		body.Request.Source.Location.RemoteURL = fmt.Sprintf("https://10.%d.%d.%d/repo.git", a, b, c)
		return f.intake(f.token, body).Code == 400
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 48}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := teststore.SQL(t, f.st).QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("private source persisted runs: %d: %v", count, err)
	}
}
