package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestWingStatusAuthenticatedTeamResults(t *testing.T) {
	s, session := credentialSessionServer(t)
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var token credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &token) != nil {
		t.Fatalf("issue: %d %s", w.Code, w.Body)
	}
	engineSQL(t, s.engine.Store, `UPDATE runs SET git_branch='main',repo_display='acme/repo',status='failed',finished_at=1000000000,error='private-error' WHERE team='a'`)
	engineSQL(t, s.engine.Store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
	engineSQL(t, s.engine.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,git_branch,repo_display,finished_at,log_retention_class,log_retention_days) SELECT 'foreign','foreign-secret',1,'foreign-pipeline','failed',request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,'main','foreign-repo',finished_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
	w = engineClaimHTTP(t, h, token.Capability, "GET", "/api/status?team=foreign", "", 200)
	if strings.Contains(w.Body.String(), "foreign") || strings.Contains(w.Body.String(), "private-error") {
		t.Fatalf("private data: %s", w.Body)
	}
	var doc controllerwire.WingStatus
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Source != "sparkwing" || doc.State != "fail" || len(doc.Items) != 4 {
		t.Fatalf("status: %+v", doc)
	}
	var pipeline controllerwire.WingStatusItem
	for _, item := range doc.Items {
		if item.Name == "acme/repo / ci" {
			pipeline = item
		}
	}
	if pipeline.State != "fail" || pipeline.UpdatedAt != "1970-01-01T00:00:01Z" || !strings.HasSuffix(pipeline.URL, "/runs?run=run") {
		t.Fatalf("pipeline: %+v", pipeline)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable status")
	}
	if _, err := time.Parse(time.RFC3339, doc.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "acme/repo") {
		t.Fatalf("anonymous: %d %s", rec.Code, rec.Body)
	}
}

func TestWingStatusPipelineSelection(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	now := time.Now().UTC()
	engineSQL(t, e.Store, `UPDATE runs SET git_branch='main',repo_display='repo',status='failed',finished_at=1,created_at=1 WHERE team='a'`)
	add := func(id, repo, pipeline, branch, status string, number int) {
		t.Helper()
		engineSQL(t, e.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,git_branch,repo_display,retry_of,finished_at,log_retention_class,log_retention_days) SELECT team,?,?,?, ?,request_json,request_hash,request_sealed_at,?,dispatch,intake_mode,created_principal,?,?,?,'',1,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, id, number, pipeline, status, repo, number, branch, repo)
	}
	add("retry", "gh:1", "ci", "refs/heads/main", "success", 2)
	engineSQL(t, e.Store, `UPDATE runs SET retry_of='run' WHERE id='retry'`)
	add("feature", "gh:1", "ci", "feature", "failed", 3)
	add("other-repo", "gh:2", "ci", "main", "failed", 4)
	add("trunk", "gh:3", "deploy", "trunk", "success", 5)
	engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE id='trunk'`, []byte(`{"request":{"git":{"default_branch":{"set":true,"value":"trunk"}}}}`))
	add("child", "gh:1", "child", "main", "failed", 6)
	engineSQL(t, e.Store, `UPDATE runs SET parent_run_id='run' WHERE id='child'`)
	rows, err := tenant.PipelineHealth(t.Context(), 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[row.ID] = pipelineStatusItem(row, "http://localhost", now).State
	}
	if got["retry"] != "warn" || got["other-repo"] != "fail" || got["trunk"] != "ok" {
		t.Fatalf("states: %v", got)
	}
	rows, err = tenant.PipelineHealth(t.Context(), 1)
	if err != nil || len(rows) != 2 {
		t.Fatalf("lookahead: %v %v", rows, err)
	}
	if _, err := teststore.SQL(t, e.Store).ExecContext(t.Context(), `UPDATE runs SET status='pending',finished_at=NULL WHERE id='trunk'`); err != nil {
		t.Fatal(err)
	}
	if n, err := tenant.PendingRunCount(t.Context()); err != nil || n != 1 {
		t.Fatalf("pending %d %v", n, err)
	}
}

func TestWingStatusLongRunAndCompletion(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status, retry string
		age           time.Duration
		want          string
	}{
		{"success", "", 0, "ok"}, {"success", "old", 0, "warn"}, {"failed", "", 0, "fail"}, {"running", "", time.Hour, "warn"}, {"running", "", time.Minute, "unknown"}, {"cancelled", "", 0, "warn"},
	} {
		item := pipelineStatusItem(store.PipelineHealthRun{Status: tc.status, RetryOf: tc.retry, CreatedAt: now.Add(-tc.age)}, "http://localhost", now)
		if item.State != tc.want || item.UpdatedAt != "" {
			t.Fatalf("%+v: %+v", tc, item)
		}
	}
}

func TestWingStatusDaemonFailureIsSanitized(t *testing.T) {
	s, session := credentialSessionServer(t)
	s.WithLocalAdmission(func(context.Context) (wingwire.QueueState, error) {
		return wingwire.QueueState{}, errors.New("private socket path")
	})
	h := s.Handler()
	w := credentialRequestHTTP(t, h, session, `{"grants":["runs.read"]}`, false)
	var token credentialResponse
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &token) != nil {
		t.Fatal("token issuance")
	}
	w = engineClaimHTTP(t, h, token.Capability, "GET", "/api/status", "", 200)
	var doc controllerwire.WingStatus
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.State != "fail" || strings.Contains(w.Body.String(), "private socket") {
		t.Fatalf("response: %s", w.Body)
	}
}
