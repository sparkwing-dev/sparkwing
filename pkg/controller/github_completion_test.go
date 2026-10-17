package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/quick"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/githubapp"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type completionGitHub struct {
	mu       sync.Mutex
	checks   []githubapp.CheckRun
	statuses []githubCommitStatusRequest
	tokens   []struct {
		IDs         []int64           `json:"repository_ids"`
		Names       []string          `json:"repositories"`
		Permissions map[string]string `json:"permissions"`
	}
	paths         []string
	repository    int64
	name          string
	existing      int64
	failWrites    int
	checksGranted bool
	before        func(*http.Request)
}

func (g *completionGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	if g.before != nil {
		g.before(r)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paths = append(g.paths, r.URL.Path)
	status, payload := 200, `{}`
	switch {
	case r.URL.Path == "/app/installations/7/access_tokens":
		var body struct {
			IDs         []int64           `json:"repository_ids"`
			Names       []string          `json:"repositories"`
			Permissions map[string]string `json:"permissions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		g.tokens = append(g.tokens, body)
		if !g.checksGranted && body.Permissions["checks"] != "" {
			status, payload = 422, `{"message":"The permissions requested are not granted to this installation"}`
		} else {
			payload = `{"token":"private","expires_at":"2100-01-01T00:00:00Z"}`
		}
	case r.URL.Path == "/repositories/42":
		payload = fmt.Sprintf(`{"id":%d,"full_name":%q}`, g.repository, g.name)
	case strings.Contains(r.URL.Path, "/commits/") && strings.HasSuffix(r.URL.Path, "/check-runs"):
		payload = `{"check_runs":[]}`
		if g.existing > 0 {
			payload = fmt.Sprintf(`{"check_runs":[{"id":%d,"external_id":"run"}]}`, g.existing)
		}
	case strings.HasPrefix(r.URL.Path, "/repos/"+g.name+"/check-runs"):
		if g.failWrites > 0 {
			g.failWrites--
			status = 502
			break
		}
		var check githubapp.CheckRun
		if err := json.NewDecoder(r.Body).Decode(&check); err != nil {
			return nil, err
		}
		g.checks = append(g.checks, check)
		if g.existing == 0 {
			g.existing = 101
		}
		payload = fmt.Sprintf(`{"id":%d}`, g.existing)
	case strings.HasPrefix(r.URL.Path, "/repos/"+g.name+"/statuses/"):
		if g.failWrites > 0 {
			g.failWrites--
			status = 502
			break
		}
		var commit githubCommitStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&commit); err != nil {
			return nil, err
		}
		g.statuses = append(g.statuses, commit)
	default:
		return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
}

type githubCompletionFixture struct {
	t      *testing.T
	server *Server
	tenant *store.Tenant
	api    *completionGitHub
	cfg    githubapp.Config
	now    time.Time
	finish func(store.RunState)
}

func newGitHubCompletion(t *testing.T) *githubCompletionFixture {
	t.Helper()
	server, tenant, finish := preparedProfileRun(t)
	f := &githubCompletionFixture{t: t, server: server, tenant: tenant, finish: finish, now: time.Now(), api: &completionGitHub{repository: 42, name: "acme/app", checksGranted: true}}
	server.store.SetTestClock(t, func() time.Time { return f.now })
	f.changeRequest(func(input *runspec.Request) {
		input.Git = runspec.Git{Repository: runspec.Repository{Key: "gh:42", Display: "acme/app"}, SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", App: runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{InstallationID: 7, RepositoryID: 42, SHA: strings.Repeat("a", 40), Ref: "refs/heads/main", DeliveryKey: "verified", VerifiedAt: f.now}}}
	})
	if _, err := tenant.BindGitHubAppInstallation(t.Context(), store.GitHubAppInstallation{InstallationID: 7, AccountID: 70, AccountLogin: "acme", AccountType: "Organization"}, f.now); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = githubapp.Config{AppID: 1, PrivateKey: key, HTTP: &http.Client{Transport: f.api}}
	server.WithGitHubApp(f.cfg).WithDashboardURL("https://dash.example.test")
	return f
}

func (f *githubCompletionFixture) changeRequest(change func(*runspec.Request)) {
	f.t.Helper()
	run, err := f.tenant.ReadRun(f.t.Context(), "run")
	if err != nil {
		f.t.Fatal(err)
	}
	change(&run.Request.Request)
	sealed, err := json.Marshal(run.Request)
	if err != nil {
		f.t.Fatal(err)
	}
	engineSQL(f.t, f.server.store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, sealed)
}

func (f *githubCompletionFixture) reconcile() {
	f.t.Helper()
	if err := f.server.ReconcileRunEffects(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *githubCompletionFixture) revision() (desired, delivered, attempts, next int64) {
	f.t.Helper()
	if err := f.server.store.DB().QueryRow(`SELECT desired_rev,delivered_rev,attempts,next_at FROM run_effects WHERE team='a' AND run_id='run' AND effect='github_check'`).Scan(&desired, &delivered, &attempts, &next); err != nil {
		f.t.Fatal(err)
	}
	return
}

func TestGitHubCompletionFromRealTerminalTransition(t *testing.T) {
	for _, outcome := range []store.RunState{store.RunSuccess, store.RunFailed, store.RunCancelled} {
		t.Run(string(outcome), func(t *testing.T) {
			f := newGitHubCompletion(t)
			f.finish(outcome)
			for range 3 {
				f.reconcile()
			}
			checks := f.api.checks
			if len(checks) != 1 || checks[0].Status != "completed" || checks[0].Conclusion != githubCheckConclusion(string(outcome)) || checks[0].ExternalID != "run" || checks[0].DetailsURL != "https://dash.example.test/runs?run=run" {
				t.Fatalf("terminal GitHub checks=%+v", checks)
			}
			if checks[0].HeadSHA != strings.Repeat("a", 40) || len(f.api.statuses) != 0 {
				t.Fatalf("commit=%+v statuses=%+v", checks[0], f.api.statuses)
			}
			for _, token := range f.api.tokens {
				if len(token.IDs) != 1 || token.IDs[0] != 42 || len(token.Names) != 0 || len(token.Permissions) != 1 || token.Permissions["checks"] != "write" {
					t.Fatalf("unscoped token=%+v", token)
				}
			}
			desired, delivered, attempts, _ := f.revision()
			if desired != delivered || desired != 1 || attempts != 0 {
				t.Fatalf("revisions=%d/%d attempts=%d", delivered, desired, attempts)
			}
			p, err := f.tenant.GetPipelineProfile(t.Context(), "ci", "build")
			if err != nil || outcome == store.RunSuccess && (p == nil || p.SampleCount != 1) || outcome != store.RunSuccess && p != nil {
				t.Fatalf("profile=%+v: %v", p, err)
			}
		})
	}
}

func TestGitHubCompletionRetriesAfterControllerReplacement(t *testing.T) {
	f := newGitHubCompletion(t)
	f.finish(store.RunSuccess)
	f.api.failWrites = 2
	var logs bytes.Buffer
	f.server.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	f.reconcile()
	desired, delivered, attempts, next := f.revision()
	if desired != 1 || delivered != 0 || attempts != 1 || next != f.now.Add(time.Second).UnixNano() || len(f.api.checks) != 0 {
		t.Fatalf("failed delivery=%d/%d attempts=%d next=%d", delivered, desired, attempts, next)
	}
	f.reconcile()
	if len(f.api.tokens) != 1 {
		t.Fatalf("retry ignored backoff: %+v", f.api.tokens)
	}
	f.now = f.now.Add(time.Second)
	f.reconcile()
	_, delivered, attempts, next = f.revision()
	if delivered != 0 || attempts != 2 || next != f.now.Add(2*time.Second).UnixNano() {
		t.Fatalf("second failure=%d attempts=%d next=%d", delivered, attempts, next)
	}
	run, err := f.tenant.ReadRun(t.Context(), "run")
	if err != nil || run.State != store.RunSuccess || strings.Count(logs.String(), "deliver run effect") != 2 {
		t.Fatalf("GitHub failure changed run=%s: %v logs=%s", run.State, err, logs.String())
	}
	f.server = New(f.server.store, nil).WithGitHubApp(f.cfg)
	f.now = f.now.Add(2 * time.Second)
	f.reconcile()
	_, delivered, attempts, _ = f.revision()
	if delivered != 1 || attempts != 2 || len(f.api.checks) != 1 || f.api.checks[0].Conclusion != "success" {
		t.Fatalf("replacement delivery=%d attempts=%d checks=%+v", delivered, attempts, f.api.checks)
	}
}

func TestGitHubCompletionAcknowledgesOnlyRevisionSent(t *testing.T) {
	f := newGitHubCompletion(t)
	engineSQL(t, f.server.store, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','run','github_check','running',1,1)`)
	sent := false
	f.api.before = func(r *http.Request) {
		if !sent && r.Method == "POST" && r.URL.Path == "/repos/acme/app/check-runs" {
			sent = true
			f.finish(store.RunFailed)
		}
	}
	f.reconcile()
	desired, delivered, _, _ := f.revision()
	if !sent || desired != 2 || delivered != 1 || len(f.api.checks) != 1 || f.api.checks[0].Status != "in_progress" {
		t.Fatalf("in-flight revisions=%d/%d checks=%+v", delivered, desired, f.api.checks)
	}
	f.reconcile()
	_, delivered, _, _ = f.revision()
	if delivered != 2 || len(f.api.checks) != 2 || f.api.checks[1].Conclusion != "failure" {
		t.Fatalf("terminal revision=%d checks=%+v", delivered, f.api.checks)
	}
}

func TestGitHubCompletionAdoptsUnrecordedCreate(t *testing.T) {
	f := newGitHubCompletion(t)
	f.finish(store.RunSuccess)
	engineSQL(t, f.server.store, `CREATE TRIGGER refuse_check_record BEFORE UPDATE OF github_check_run_id ON triggers WHEN NEW.github_check_run_id>0 BEGIN SELECT RAISE(ABORT,'record refused'); END`)
	f.reconcile()
	if len(f.api.checks) != 1 || f.api.existing != 101 {
		t.Fatalf("create=%+v", f.api.checks)
	}
	_, delivered, attempts, _ := f.revision()
	if delivered != 0 || attempts != 1 {
		t.Fatalf("failed record acknowledged=%d attempts=%d", delivered, attempts)
	}
	engineSQL(t, f.server.store, `DROP TRIGGER refuse_check_record`)
	f.now = f.now.Add(time.Second)
	f.reconcile()
	if len(f.api.checks) != 2 || f.api.checks[1].HeadSHA != "" {
		t.Fatalf("adoption created another check: %+v", f.api.checks)
	}
	if id, err := f.tenant.GitHubCheckRun(t.Context(), "run"); err != nil || id != 101 {
		t.Fatalf("record=%d: %v", id, err)
	}
}

func TestGitHubCompletionFallbackSurvivesReplacement(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.checksGranted = false
	engineSQL(t, f.server.store, `UPDATE runs SET status='pending' WHERE team='a' AND id='run'`)
	engineSQL(t, f.server.store, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','run','github_check','pending',1,1)`)
	f.reconcile()
	if len(f.api.statuses) != 1 || f.api.statuses[0].State != "pending" {
		t.Fatalf("pending fallback=%+v", f.api.statuses)
	}
	engineSQL(t, f.server.store, `UPDATE runs SET status='running' WHERE team='a' AND id='run'`)
	f.server = New(f.server.store, nil).WithGitHubApp(f.cfg)
	f.api.checksGranted = true
	f.finish(store.RunSuccess)
	f.reconcile()
	if len(f.api.checks) != 0 || len(f.api.statuses) != 2 || f.api.statuses[1].State != "success" {
		t.Fatalf("fallback changed after replacement: checks=%+v statuses=%+v", f.api.checks, f.api.statuses)
	}
}

func TestGitHubCompletionUsesCurrentNumericRepository(t *testing.T) {
	f := newGitHubCompletion(t)
	f.api.name = "renamed/new-name"
	f.changeRequest(func(input *runspec.Request) { input.Git.Repository.Display = "reused/old-name" })
	f.finish(store.RunSuccess)
	f.reconcile()
	if len(f.api.checks) != 1 {
		t.Fatalf("renamed repository checks=%+v paths=%v", f.api.checks, f.api.paths)
	}
	for _, path := range f.api.paths {
		if strings.Contains(path, "reused") || strings.Contains(path, "/repos/acme/app/") {
			t.Fatalf("delivery used an unverified name: %s", path)
		}
	}
}

func TestGitHubCompletionAuthorityProperty(t *testing.T) {
	property := func(scenario uint8, display string) bool {
		f := newGitHubCompletion(t)
		f.changeRequest(func(input *runspec.Request) {
			input.Git.Repository.Display = display
			switch scenario % 10 {
			case 0:
				input.Git.App.Value.RepositoryID = 43
			case 1:
				input.Git.App.Value.SHA = strings.Repeat("b", 40)
			case 2:
				input.Git.App.Value.Ref = "refs/heads/foreign"
			case 3:
				input.Git.App.Value.DeliveryKey = ""
			case 4:
				input.Git.App.Value.VerifiedAt = time.Time{}
			case 5:
				input.Git.App.Value.InstallationID = 0
			}
		})
		switch scenario % 10 {
		case 6:
			engineSQL(t, f.server.store, `UPDATE github_app_installations SET suspended=1 WHERE installation_id=7`)
		case 7:
			engineSQL(t, f.server.store, `UPDATE github_app_installations SET team='default' WHERE installation_id=7`)
		case 8:
			f.api.repository = 43
		case 9:
			f.api.before = func(r *http.Request) {
				if r.URL.Path == "/repositories/42" {
					engineSQL(t, f.server.store, `UPDATE github_app_installations SET suspended=1 WHERE installation_id=7`)
				}
			}
		}
		f.finish(store.RunSuccess)
		f.reconcile()
		_, delivered, attempts, _ := f.revision()
		return len(f.api.checks) == 0 && len(f.api.statuses) == 0 && delivered == 0 && attempts == 1
	}
	for scenario := range uint8(10) {
		if !property(scenario, "untrusted/reused") {
			t.Fatalf("authority scenario %d sent a foreign check", scenario)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 20}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubCompletionStalePhasesNeverRegressProperty(t *testing.T) {
	property := func(outcome uint8, phase bool, statuses bool) bool {
		f := newGitHubCompletion(t)
		f.api.checksGranted = !statuses
		state := []store.RunState{store.RunSuccess, store.RunFailed, store.RunCancelled}[outcome%3]
		f.finish(state)
		f.reconcile()
		currentChecks, currentStatuses := len(f.api.checks), len(f.api.statuses)
		old := store.RunPending
		if phase {
			old = store.RunRunning
		}
		u, relevant, err := f.server.githubAppCheckUpdate(t.Context(), f.tenant, store.DesiredRunEffect{RunID: "run", State: old})
		if err != nil || !relevant {
			t.Fatalf("stale fixture: %v", err)
		}
		reporter := githubCheckReporter{client: f.server.githubApp.client, store: f.server.store}
		if err := reporter.deliver(t.Context(), u); err != nil {
			t.Fatal(err)
		}
		return len(f.api.checks) == currentChecks && len(f.api.statuses) == currentStatuses
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubCompletionSummaryPrivacyProperty(t *testing.T) {
	property := func(raw []byte) bool {
		f := newGitHubCompletion(t)
		private := "private-" + hex.EncodeToString(raw)
		plan, err := json.Marshal(map[string]any{"jobs": []map[string]string{{"id": "build"}, {"id": private}}})
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, f.server.store, `UPDATE runs SET plan_json=? WHERE team='a' AND id='run'`, plan)
		engineSQL(t, f.server.store, `UPDATE runs SET error=? WHERE team='a' AND id='run'`, private)
		if err := teststore.SeedNode(f.server.store, t.Context(), store.Node{RunID: "run", NodeID: private, Status: "pending", Error: private}); err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedNodeFinish(f.server.store, t.Context(), f.tenant.Team(), store.NodeRef{RunID: "run", NodeID: private}, store.NodeResult{Outcome: store.NodeFailed, MaskedError: private}); err != nil {
			t.Fatal(err)
		}
		f.finish(store.RunFailed)
		f.reconcile()
		if len(f.api.checks) != 1 {
			t.Fatalf("checks=%+v", f.api.checks)
		}
		encoded, err := json.Marshal(f.api.checks[0])
		if err != nil {
			t.Fatal(err)
		}
		output := f.api.checks[0].Output
		return !strings.Contains(string(encoded), private) && utf8.Valid(encoded) && len(output.Summary) <= 65535 && strings.Contains(output.Summary, "2 failed")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubCompletionCancellationLeavesDurableWork(t *testing.T) {
	f := newGitHubCompletion(t)
	f.finish(store.RunSuccess)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.server.ReconcileRunEffects(ctx); err == nil {
		t.Fatal("cancelled completion reported success")
	}
	_, delivered, _, _ := f.revision()
	if delivered != 0 || len(f.api.checks) != 0 {
		t.Fatal("cancelled completion lost its work")
	}
	f.reconcile()
	if len(f.api.checks) != 1 {
		t.Fatalf("subsequent completion=%+v", f.api.checks)
	}
}

func TestGitHubCompletionTwoControllersDeliverOnce(t *testing.T) {
	f := newGitHubCompletion(t)
	f.finish(store.RunSuccess)
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() { unblock.Do(func() { close(release) }) })
	f.api.before = func(r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/repos/acme/app/check-runs" {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { done <- f.server.ReconcileRunEffects(t.Context()) }()
	<-entered
	other := New(f.server.store, nil).WithGitHubApp(f.cfg)
	if err := other.ReconcileRunEffects(t.Context()); err != nil {
		t.Fatal(err)
	}
	unblock.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(f.api.checks) != 1 {
		t.Fatalf("two controllers created checks=%+v", f.api.checks)
	}
}

func TestGitHubCompletionReaperPreservesTimeoutConclusion(t *testing.T) {
	for _, reason := range []string{"timeout", "queue_timeout"} {
		t.Run(reason, func(t *testing.T) {
			f := newGitHubCompletion(t)
			if reason == "timeout" {
				f.now = f.now.Add(time.Hour)
				engineSQL(t, f.server.store, `UPDATE triggers SET lease_expires_at=?,expires_at=? WHERE team='a' AND id='run'`, f.now.Add(-time.Second).UnixNano(), f.now.Add(-time.Second).UnixNano())
			} else {
				f.now = f.now.Add(2 * time.Minute)
				f.changeRequest(func(input *runspec.Request) {
					input.Options.ClaimWait = runspec.Setting[time.Duration]{Optional: runspec.Optional[time.Duration]{Set: true, Value: time.Second}}
				})
				engineSQL(t, f.server.store, `UPDATE runs SET status='pending',sealed_at=NULL,spec_json=NULL,spec_hash='',plan_json=NULL,plan_hash='',claim_wait_ns=? WHERE team='a' AND id='run'`, int64(time.Second))
				engineSQL(t, f.server.store, `UPDATE triggers SET status='pending',claimed_by=NULL,capability_prefix='',started_at=NULL,lease_expires_at=NULL WHERE team='a' AND id='run'`)
			}
			lease, err := f.server.store.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "test", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.server.store.ReapUnits(t.Context(), lease, 10); err != nil {
				t.Fatal(err)
			}
			if err := f.server.store.AsOperator().ReleaseSingletonLease(t.Context(), lease); err != nil {
				t.Fatal(err)
			}
			f.reconcile()
			if len(f.api.checks) != 1 || f.api.checks[0].Conclusion != "timed_out" {
				t.Fatalf("reaped run checks=%+v", f.api.checks)
			}
			u, _, err := f.server.githubAppCheckUpdate(t.Context(), f.tenant, store.DesiredRunEffect{RunID: "run", State: store.RunPending})
			if err != nil {
				t.Fatal(err)
			}
			reporter := githubCheckReporter{client: f.server.githubApp.client, store: f.server.store}
			if err := reporter.deliver(t.Context(), u); err != nil {
				t.Fatal(err)
			}
			if len(f.api.checks) != 1 {
				t.Fatalf("stale pending report overwrote timeout: %+v", f.api.checks)
			}

		})
	}
}

func TestGitHubCompletionDelayedPendingLeavesRunningCheck(t *testing.T) {
	f := newGitHubCompletion(t)
	engineSQL(t, f.server.store, `INSERT INTO run_effects(team,run_id,effect,desired_state,desired_rev,next_at) VALUES ('a','run','github_check','running',1,1)`)
	f.reconcile()
	if len(f.api.checks) != 1 || f.api.checks[0].Status != "in_progress" {
		t.Fatalf("running check=%+v", f.api.checks)
	}
	u, _, err := f.server.githubAppCheckUpdate(t.Context(), f.tenant, store.DesiredRunEffect{RunID: "run", State: store.RunPending})
	if err != nil {
		t.Fatal(err)
	}
	reporter := githubCheckReporter{client: f.server.githubApp.client, store: f.server.store}
	if err := reporter.deliver(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if len(f.api.checks) != 1 {
		t.Fatalf("stale pending report overwrote running: %+v", f.api.checks)
	}
}

func TestGitHubCompletionRejectsForeignTenant(t *testing.T) {
	f := newGitHubCompletion(t)
	f.finish(store.RunSuccess)
	foreign, err := f.server.store.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if _, relevant, err := f.server.githubAppCheckUpdate(t.Context(), foreign, store.DesiredRunEffect{RunID: "run", State: store.RunSuccess}); err == nil || relevant {
		t.Fatalf("foreign run resolution=%t, %v", relevant, err)
	}
	if len(f.api.paths) != 0 {
		t.Fatalf("foreign tenant reached GitHub: %v", f.api.paths)
	}
}

func TestGitHubCompletionUnverifiedRunReportsNothing(t *testing.T) {
	f := newGitHubCompletion(t)
	f.changeRequest(func(input *runspec.Request) { input.Git.App = runspec.Optional[runspec.AppFacts]{} })
	f.finish(store.RunSuccess)
	f.reconcile()
	_, delivered, attempts, _ := f.revision()
	if delivered != 1 || attempts != 0 || len(f.api.paths) != 0 {
		t.Fatalf("unverified run delivery=%d attempts=%d requests=%v", delivered, attempts, f.api.paths)
	}
}

func TestGitHubCompletionMissingAppKeepsTerminalDue(t *testing.T) {
	f := newGitHubCompletion(t)
	f.finish(store.RunSuccess)
	f.server = New(f.server.store, nil)
	f.reconcile()
	_, delivered, attempts, _ := f.revision()
	if delivered != 0 || attempts != 1 || len(f.api.paths) != 0 {
		t.Fatalf("unconfigured App delivery=%d attempts=%d requests=%v", delivered, attempts, f.api.paths)
	}
	profile, err := f.tenant.GetPipelineProfile(t.Context(), "ci", "build")
	if err != nil || profile == nil || profile.SampleCount != 1 {
		t.Fatalf("unconfigured App stopped profile delivery: %+v %v", profile, err)
	}
	f.server.WithGitHubApp(f.cfg)
	f.now = f.now.Add(time.Second)
	f.reconcile()
	if len(f.api.checks) != 1 {
		t.Fatalf("restored App lost terminal work: %+v", f.api.checks)
	}
}
