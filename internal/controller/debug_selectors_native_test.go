package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func debugSelectorClient(t *testing.T, s *Server, session, grants string) (controllerwire.DebugControl, string) {
	t.Helper()
	options, prefix := selectorClientOptions(t, s, session, grants)
	c, err := client.NewDebugControl(options)
	if err != nil {
		t.Fatal(err)
	}
	return c, prefix
}

func selectorClientOptions(t *testing.T, s *Server, session, grants string) (client.EngineOptions, string) {
	t.Helper()
	w := credentialRequestHTTP(t, s.Handler(), session, grants, false)
	var issued credentialResponse
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
		t.Fatalf("issue authority: HTTP %d %s", w.Code, w.Body)
	}
	return client.EngineOptions{URL: "https://controller.test", HTTP: &http.Client{Transport: localHostRoundTripper{s.Handler()}}, Bearer: func(context.Context) (string, error) { return issued.Capability, nil }}, issued.Prefix
}

func TestDebugNumericSelectorsThroughPersistedController(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	c, _ := debugSelectorClient(t, s, session, `{"grants":["runs.read","runs.control"]}`)
	for _, selector := range []string{"run", "1"} {
		job, err := c.ReadDebugJob(t.Context(), selector, "job")
		if err != nil || job.RunID != "run" || job.Number != 1 || job.Job.ID != "job" || job.Execution != nil {
			t.Fatalf("job %q: %+v %v", selector, job, err)
		}
		pause, err := c.ReadDebugPause(t.Context(), selector, "job")
		if err != nil || pause.RunID != "run" || pause.Number != 1 || pause.Pause == nil {
			t.Fatalf("pause %q: %+v %v", selector, pause, err)
		}
	}
	if err := c.ResumeDebugPause(t.Context(), "1", "job"); err != nil {
		t.Fatal(err)
	}
	pause, err := c.ReadDebugPause(t.Context(), "1", "job")
	if err != nil || pause.Pause != nil {
		t.Fatalf("released pause: %+v %v", pause, err)
	}
}

func TestDebugCurrentExecutionProjection(t *testing.T) {
	s, session := pauseHTTPFixture(t)
	c, _ := debugSelectorClient(t, s, session, `{"grants":["runs.read"]}`)
	engineSQL(t, s.store, `UPDATE nodes SET status='running',generation=2,attempt=3,claimed_by='private-holder',capability_prefix='private-prefix',lease_expires_at=?,spec_json=? WHERE team='a' AND run_id='run' AND node_id='job'`, time.Now().Add(time.Hour).UnixNano(), []byte(`{"id":"job","env":{"PRIVATE":"private-environment"}}`))
	engineSQL(t, s.store, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES ('a','run','job',3,2,'private-holder','private-prefix','runner','public-runner','private-location',1)`)
	for _, execution := range []struct{ persisted, location, public string }{
		{"runner", "private-location", "runner"},
		{"local", "local", "local"},
		{"coordinator", "local", "local"},
		{"coordinator", "unknown", ""},
		{"agent", "remote", "runner"},
		{"cloud", "cloud", ""},
		{"kubernetes", "private-location", ""},
	} {
		engineSQL(t, s.store, `UPDATE node_attempts SET executor_kind=?,executor_location=?`, execution.persisted, execution.location)
		job, err := c.ReadDebugJob(t.Context(), "1", "job")
		if err != nil || execution.public != "" && (job.Execution == nil || job.Execution.Kind != execution.public || job.Execution.Runner != "public-runner") || execution.public == "" && job.Execution != nil {
			t.Fatalf("execution %q: %+v %v", execution.persisted, job, err)
		}
		raw, err := json.Marshal(job)
		if err != nil || bytes.Contains(raw, []byte("private-")) || bytes.Contains(raw, []byte("capability")) || bytes.Contains(raw, []byte("spec")) {
			t.Fatalf("private execution metadata escaped projection: %s %v", raw, err)
		}
	}
	engineSQL(t, s.store, `UPDATE nodes SET lease_expires_at=1`)
	job, err := c.ReadDebugJob(t.Context(), "1", "job")
	if err != nil || job.Execution != nil {
		t.Fatalf("expired execution: %+v %v", job, err)
	}
}

func FuzzDebugSelectorsStayWithinAuthenticatedTeam(f *testing.F) {
	for _, selector := range []string{"run", "1", "2", "foreign-run", "01", "+1", "-1", "9223372036854775808", "1?team=b", "../foreign-run", "' OR 1=1 --"} {
		f.Add(selector, byte(0))
	}
	f.Add("1", byte(1))
	f.Add("run", byte(2))
	f.Add("1", byte(3))
	f.Fuzz(func(t *testing.T, selector string, state byte) {
		if len(selector) > 512 {
			t.Skip()
		}
		s, session := pauseHTTPFixture(t)
		engineSQL(t, s.store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('b',1,1)`)
		engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','foreign-run',1,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
		engineSQL(t, s.store, `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json) VALUES ('b','foreign-run','job','pending',?,?)`, []byte(`{}`), []byte(`{}`))
		engineSQL(t, s.store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'b','foreign-only',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
		engineSQL(t, s.store, `INSERT INTO debug_pauses(team,run_id,node_id,reason,paused_at,expires_at) SELECT 'b','foreign-run','job',reason,paused_at,expires_at FROM debug_pauses WHERE team='a' AND run_id='run' AND node_id='job'`)
		grants := `{"grants":["runs.read","runs.control"]}`
		if state%4 == 3 {
			grants = `{"grants":["runs.control"]}`
		}
		options, prefix := selectorClientOptions(t, s, session, grants)
		c, err := client.NewDebugControl(options)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := client.NewRunReader(options)
		if err != nil {
			t.Fatal(err)
		}
		control, err := client.NewEngineClient(options)
		if err != nil {
			t.Fatal(err)
		}
		switch state % 4 {
		case 1:
			engineSQL(t, s.store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, prefix)
		case 2:
			engineSQL(t, s.store, `DELETE FROM memberships WHERE team='a' AND account_id='member'`)
		}
		job, jobErr := c.ReadDebugJob(t.Context(), selector, "job")
		pause, pauseErr := c.ReadDebugPause(t.Context(), selector, "job")
		wantRead := state%4 == 0 && (selector == "run" || selector == "1")
		if (jobErr == nil) != wantRead || (pauseErr == nil) != wantRead {
			t.Fatalf("selector %q state %d: job=%+v %v pause=%+v %v", selector, state%4, job, jobErr, pause, pauseErr)
		}
		if wantRead && (job.RunID != "run" || pause.RunID != "run" || job.Number != 1 || pause.Number != 1) {
			t.Fatalf("selector escaped authenticated team: %+v %+v", job, pause)
		}
		if _, err := reader.ReadSteps(t.Context(), selector); (err == nil) != wantRead {
			t.Fatalf("steps %q state %d: %v", selector, state%4, err)
		}
		err = c.ResumeDebugPause(t.Context(), selector, "job")
		wantWrite := (state%4 == 0 || state%4 == 3) && (selector == "run" || selector == "1")
		if (err == nil) != wantWrite {
			t.Fatalf("release %q state %d: %v", selector, state%4, err)
		}
		wantReleased := 0
		if wantWrite {
			wantReleased = 1
		}
		var released int
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM debug_pauses WHERE released_at IS NOT NULL`).Scan(&released); err != nil || released != wantReleased {
			t.Fatalf("release committed after invalid selection: %d %v", released, err)
		}
		if _, err := control.Cancel(t.Context(), selector, controllerwire.CancelRequest{Reason: "user_cancelled"}); (err == nil) != wantWrite {
			t.Fatalf("cancel %q state %d: %v", selector, state%4, err)
		}
		var cancelled int
		if err := teststore.SQL(t, s.store).QueryRow(`SELECT COUNT(*) FROM runs WHERE cancel_requested_at IS NOT NULL`).Scan(&cancelled); err != nil || cancelled != wantReleased {
			t.Fatalf("cancellation escaped authorized selection: %d %v", cancelled, err)
		}
	})
}
