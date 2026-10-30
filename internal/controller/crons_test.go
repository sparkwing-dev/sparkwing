package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

// bug: git's local fixture redirect happens after clone-host validation, so a named host would still require DNS.
const cronTestRepoURL = "https://8.8.8.8/acme/widgets.git"

const cronTestSHA = "cf857896cdbc5330e7600465c2a6dade81d8bcba"

type cronsFixture struct {
	t       *testing.T
	server  *controller.Server
	handler http.Handler
	store   *store.Store
	writer  string
	reader  string
	none    string
}

func newCronsFixture(t *testing.T, opts ...store.Option) *cronsFixture {
	t.Helper()
	remotes := []string{cronTestRepoURL}
	for i := range 11 {
		remotes = append(remotes, fmt.Sprintf("https://8.8.8.8/acme/repo-%d.git", i))
	}
	if sha := sourcetest.FixedRemote(t, remotes...); sha != cronTestSHA {
		t.Fatalf("fixed schedule commit = %s", sha)
	}
	var s *controller.Server
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"), append([]store.Option{store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		s = controller.New(st, nil)
		return s.StorePolicies(), nil
	})}, opts...)...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	writer, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "pusher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.read", "runs.control"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("CreateToken writer: %v", err)
	}
	reader, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "viewer"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.read"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}
	none, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "stranger"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.write"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	return &cronsFixture{t: t, server: s, handler: s.Handler(), store: st, writer: writer, reader: reader, none: none}
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
	req, err := http.NewRequestWithContext(context.Background(), method, path, reader)
	if err != nil {
		f.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", method+":"+path)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	resp := w.Result()
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
	f.call(http.MethodPut, "/api/v1/schedules/repos", f.writer, body, http.StatusOK, &out)
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
	if testing.Short() {
		t.Skip("slow: 1.7s measured in the short class; the full suite runs it")
	}
	f := newCronsFixture(t)
	f.push(cronPushBody())
	const path = "/api/v1/schedules/acme%2Fwidgets%2Fnightly"

	var launched controllerwire.ScheduleRun
	f.call(http.MethodPost, path+"/run", f.writer, nil, http.StatusOK, &launched)
	if launched.RunID == "" {
		t.Fatal("run now named no run")
	}

	ctx := context.Background()
	tenant := teststore.ForTeam(t, f.store, store.DefaultTeam)
	run, err := tenant.ReadRun(ctx, launched.RunID)
	if err != nil {
		t.Fatal(err)
	}
	request := run.Request.Request
	if request.Origin != runspec.OriginSchedule || request.Actor.ID != "pusher" {
		t.Errorf("scheduled origin/actor = %s/%+v", request.Origin, request.Actor)
	}
	if request.Source.Location.RemoteURL != cronTestRepoURL || request.Git.SHA != cronTestSHA || request.Git.Ref != "refs/heads/main" || request.Git.App.Set {
		t.Errorf("sealed source = %+v %+v", request.Git, request.Source)
	}
	if request.RequestedArgs["depth"] != "deep" {
		t.Errorf("sealed args = %v", request.RequestedArgs)
	}
	trigger, err := tenant.GetTrigger(ctx, launched.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.IdempotencyKey == "" || trigger.Status != "pending" || run.State != runstate.RunPending || run.Request.Hash == "" {
		t.Errorf("unsealed or unqueued schedule: trigger=%+v run=%+v", trigger, run)
	}
}

func TestControllerCrons_RunNowReplaysTheSameRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.0s measured in the short class; the full suite runs it")
	}
	f := newCronsFixture(t)
	f.push(cronPushBody())
	const path = "/api/v1/schedules/acme%2Fwidgets%2Fnightly/run"
	var first, second controllerwire.ScheduleRun
	f.call(http.MethodPost, path, f.writer, nil, http.StatusOK, &first)
	f.call(http.MethodPost, path, f.writer, nil, http.StatusOK, &second)
	if first.RunID == "" || first.Replayed || second.RunID != first.RunID || !second.Replayed {
		t.Fatalf("schedule replay: first=%+v second=%+v", first, second)
	}
	runs, err := teststore.ForTeam(t, f.store, store.DefaultTeam).ListRuns(t.Context(), store.RunFilter{Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].ID != first.RunID {
		t.Fatalf("replayed request persisted %d runs: %v", len(runs), err)
	}
}

type scheduleSourceProbe struct {
	store.IntakePolicy
	calls int
}

func (p *scheduleSourceProbe) ResolveScheduleSource(context.Context, store.CronSchedule, runspec.Request) (runspec.Request, error) {
	p.calls++
	return runspec.Request{}, errors.New("source lookup reached")
}

func TestControllerCrons_BoundsEntriesBeforeResolvingSource(t *testing.T) {
	probe := &scheduleSourceProbe{}
	f := newCronsFixture(t, fixtureStorePolicies(store.Policies{Intake: probe}))
	body := cronPushBody()
	entries := make([]map[string]any, 51)
	for i := range entries {
		entries[i] = map[string]any{"pipeline": fmt.Sprintf("job-%d", i), "cron": "0 3 * * *"}
	}
	body["schedules"] = entries
	var refused apierr.Envelope
	f.call(http.MethodPut, "/api/v1/schedules/repos", f.writer, body, http.StatusConflict, &refused)
	if probe.calls != 0 || refused.Error == nil || refused.Error.Code != apierr.Conflict {
		t.Fatalf("overfull declaration: refusal=%+v source calls=%d", refused, probe.calls)
	}
	for _, want := range []string{"limit is 50", "current usage is 0", "request declares 51", "support@sparkwing.dev"} {
		if !strings.Contains(refused.Error.Message, want) {
			t.Fatalf("refusal %q omits %q", refused.Error.Message, want)
		}
	}
}
