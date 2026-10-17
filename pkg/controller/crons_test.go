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

	"github.com/sparkwing-dev/sparkwing/internal/capability"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const cronTestRepoURL = "https://github.com/acme/widgets.git"

const cronTestSHA = "0123456789abcdef0123456789abcdef01234567"

type cronsFixture struct {
	t       *testing.T
	handler http.Handler
	store   *store.Store
	writer  string
	reader  string
	none    string
}

func newCronsFixture(t *testing.T) *cronsFixture {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	writer, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "pusher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.read", "runs.control"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("CreateToken writer: %v", err)
	}
	reader, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "viewer"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.read"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("CreateToken reader: %v", err)
	}
	none, _, err := teststore.ProvisionCredential(context.Background(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "stranger"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: teststore.CredentialGrants([]string{"runs.write"}), ExpiresAt: teststore.CredentialExpiry(now, 0)})
	if err != nil {
		t.Fatalf("CreateToken stranger: %v", err)
	}
	s := controller.New(st, nil)
	controller.ScheduleWriterFixture(t, s)
	return &cronsFixture{t: t, handler: s.Handler(), store: st, writer: writer, reader: reader, none: none}
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
	if request.Source.Location.RemoteURL != cronTestRepoURL || request.Git.SHA != cronTestSHA || request.Git.Ref != "main" || request.Git.App.Set {
		t.Errorf("sealed source = %+v %+v", request.Git, request.Source)
	}
	if request.RequestedArgs["depth"] != "deep" {
		t.Errorf("sealed args = %v", request.RequestedArgs)
	}
	trigger, err := tenant.GetTrigger(ctx, launched.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.IdempotencyKey == "" || trigger.Status != "pending" || run.State != store.RunPending || run.Request.Hash == "" {
		t.Errorf("unsealed or unqueued schedule: trigger=%+v run=%+v", trigger, run)
	}
}

func TestControllerCrons_RunNowReplaysTheSameRequest(t *testing.T) {
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
