package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const cronTestRepoURL = "https://github.com/acme/widgets.git"

const cronTestSHA = "0123456789abcdef0123456789abcdef01234567"

type cronsFixture struct {
	t      *testing.T
	url    string
	store  *store.Store
	writer string
	reader string
	none   string
}

func newCronsFixture(t *testing.T) *cronsFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	writer, _, err := st.CreateToken("pusher", store.TokenKindUser,
		[]string{controller.ScopeRunsRead, controller.ScopeRunsControl}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken writer: %v", err)
	}
	reader, _, err := st.CreateToken("viewer", store.TokenKindUser,
		[]string{controller.ScopeRunsRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}
	none, _, err := st.CreateToken("stranger", store.TokenKindUser,
		[]string{controller.ScopeTriggersRead}, 0, now)
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return &cronsFixture{t: t, url: srv.URL, store: st, writer: writer, reader: reader, none: none}
}

// safety: the request and its body's close live in one function, so no caller
// can leak a response by handing it on.
func (f *cronsFixture) call(method, path, token string, body any, want int, out any) int {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			f.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.url+path, reader)
	if err != nil {
		f.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if want == 0 {
		return resp.StatusCode
	}
	if resp.StatusCode != want {
		read, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("%s %s: status = %d, want %d: %s", method, path, resp.StatusCode, want, bytes.TrimSpace(read))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			f.t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode
}

// safety: a zero want means the caller judges the status itself, so nothing is
// asserted here beyond the request landing.
func (f *cronsFixture) status(method, path, token string, body any) int {
	f.t.Helper()
	return f.call(method, path, token, body, 0, nil)
}

func (f *cronsFixture) push(body map[string]any) map[string]any {
	f.t.Helper()
	var out map[string]any
	f.call(http.MethodPut, "/api/v1/crons/repos", f.writer, body, http.StatusOK, &out)
	return out
}

func cronPushBody() map[string]any {
	return map[string]any{
		"repo_url": cronTestRepoURL,
		"branch":   "main",
		"sha":      cronTestSHA,
		"schedules": []map[string]any{
			{"pipeline": "nightly", "cron": "0 3 * * *", "tz": "UTC", "args": map[string]string{"depth": "deep"}},
			{"pipeline": "sweep", "name": "quick", "cron": "*/5 * * * *"},
		},
	}
}

func TestControllerCrons_RunNowCreatesOneTriggerAndPendingRunPerDueInstant(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())
	const path = "/api/v1/crons/acme%2Fwidgets%2Fnightly"

	var launched controllerwire.ScheduleRun
	f.call(http.MethodPost, path+"/run", f.writer, nil, http.StatusOK, &launched)
	if launched.RunID == "" {
		t.Fatal("run now named no run")
	}

	ctx := context.Background()
	trigger, err := f.store.GetTrigger(ctx, launched.RunID)
	if err != nil {
		t.Fatalf("GetTrigger: %v", err)
	}
	if trigger.TriggerSource != "schedule" {
		t.Errorf("trigger source = %q, want schedule", trigger.TriggerSource)
	}
	if trigger.RepoURL != cronTestRepoURL || trigger.GitSHA != cronTestSHA || trigger.GitBranch != "main" {
		t.Errorf("trigger git = %+v, want the pushed source", trigger)
	}
	if trigger.Args["depth"] != "deep" {
		t.Errorf("trigger args = %v, want the schedule's", trigger.Args)
	}
	scheduleID := launched.Schedule.ID
	if trigger.TriggerEnv[crons.ScheduleEnvKey] != scheduleID {
		t.Errorf("trigger env = %v, want the schedule id under %s", trigger.TriggerEnv, crons.ScheduleEnvKey)
	}
	if trigger.IdempotencyKey == "" {
		t.Error("a scheduled launch carries no idempotency key")
	}
	run, err := f.store.GetRun(ctx, launched.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "pending" {
		t.Errorf("run status = %q, want pending", run.Status)
	}
}

func TestControllerCrons_ASecondLaunchOfOneDueInstantReachesTheFirstRun(t *testing.T) {
	f := newCronsFixture(t)
	f.push(cronPushBody())

	ctx := context.Background()
	svc := &crons.Service{Store: f.store, Schedules: teststore.ForTeam(t, f.store, store.DefaultTeam)}
	sched, err := svc.Resolve(ctx, "acme/widgets/nightly")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	due := time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC)
	key := controller.CronIdempotencyKey(sched.ID, due)
	if key != sched.ID+"@2026-03-01T03:00:00Z" {
		t.Fatalf("idempotency key = %q", key)
	}
	first := "run_first"
	if err := f.store.CreateTrigger(ctx, store.Trigger{
		ID: first, Pipeline: sched.Pipeline, CreatedAt: time.Now(), IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("CreateTrigger: %v", err)
	}
	found, err := f.store.FindTriggerByIdempotencyKey(ctx, sched.Pipeline, key)
	if err != nil {
		t.Fatalf("FindTriggerByIdempotencyKey: %v", err)
	}
	if found.ID != first {
		t.Errorf("resolved run = %s, want %s", found.ID, first)
	}
}
