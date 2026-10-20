package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func memberLogSearchReader(t *testing.T, st *store.Store, objects blob.ObjectBackend) (controllerwire.LogReader, string) {
	t.Helper()
	options, prefix, _ := memberObservationOptions(t, st, objects)
	reader, err := client.NewLogReader(options)
	if err != nil {
		t.Fatal(err)
	}
	return reader, prefix
}

func memberObservationOptions(t *testing.T, st *store.Store, objects blob.ObjectBackend) (client.EngineOptions, string, *Server) {
	t.Helper()
	engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	session := "private-search-session"
	digest := sha256.Sum256([]byte(session))
	engineSQL(t, st, `INSERT INTO sessions(hash,principal,scopes,created_at,expires_at,team,account_id) VALUES (?,'member','logs.read,runs.read',1,?,'a','member')`, hex.EncodeToString(digest[:]), time.Now().Add(time.Hour).UnixNano())
	engineSQL(t, st, `INSERT INTO session_membership_epochs(session_hash,team,epoch) VALUES (?,'a',0)`, hex.EncodeToString(digest[:]))
	server := New(st, nil).WithDataObjects(objects)
	authorization := server.authorization
	authorization.Authenticator = capability.NewAuthenticator(st)
	authorization.CheckCSRF = func(*http.Request, capability.Principal) error { return nil }
	server.WithAuthorization(authorization)
	w := credentialRequestHTTP(t, server.Handler(), session, `{"grants":["logs.read","runs.read"]}`, false)
	var issued credentialResponse
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &issued) != nil {
		t.Fatalf("member authority: %d %s", w.Code, w.Body)
	}
	return client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return issued.Capability, nil }}, issued.Prefix, server
}

func TestPersistedLogSearchPagesAdvanceThroughCandidateRuns(t *testing.T) {
	logs, subject, st := committedDashboardLogs(t, []byte("fatal first\nordinary\nfatal second\n"))
	engineSQL(t, st, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT team,'newer',2,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
	engineSQL(t, st, `UPDATE runs SET request_json=replace(CAST(request_json AS TEXT),'"run"','"newer"') WHERE team='a' AND id='newer'`)
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	query := controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal", MaxMatches: 1}
	first, err := reader.SearchLogs(t.Context(), query)
	if err != nil || len(first.Matches) != 0 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("empty candidate page: %+v %v", first, err)
	}
	logSearchCandidate(t, st, "later", 3, "ci")
	query.Runs.Cursor = first.NextCursor
	second, err := reader.SearchLogs(t.Context(), query)
	if err != nil || len(second.Matches) != 1 || second.Matches[0].Line != "fatal first" || second.Matches[0].Subject == nil || *second.Matches[0].Subject.Attempt != *subject.Attempt {
		t.Fatalf("persisted canonical match: %+v %v", second, err)
	}
	if second.Truncated || second.NextCursor != "" {
		t.Fatalf("finished search retained continuation: %+v", second)
	}
	query.Runs.Cursor, query.MaxMatches = "", 0
	query.Runs.Limit = 5
	all, err := reader.SearchLogs(t.Context(), query)
	if err != nil || len(all.Matches) != 2 {
		t.Fatalf("uncapped search: %+v %v", all, err)
	}
}

func TestPersistedLogSearchContinuesWithinOneSubjectWithoutRepeatingMatches(t *testing.T) {
	logs, _, st := committedDashboardLogs(t, []byte(strings.Repeat("fatal\n", logPageRecords+7)))
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	for _, limit := range []int{0, logPageRecords + 3} {
		query := controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal", MaxMatches: limit}
		first, err := reader.SearchLogs(t.Context(), query)
		if err != nil || len(first.Matches) != logPageRecords || !first.Truncated {
			t.Fatalf("first page, limit %d: %+v %v", limit, first, err)
		}
		query.Runs.Cursor = first.NextCursor
		second, err := reader.SearchLogs(t.Context(), query)
		want := 7
		if limit != 0 {
			want = 3
		}
		if err != nil || len(second.Matches) != want || second.Matches[0].LineNo != logPageRecords+1 || second.Truncated {
			t.Fatalf("remaining matches, limit %d: %+v %v", limit, second, err)
		}
	}
}

func TestPersistedLogSearchMissingCommittedBytesFailDespiteConsumedCursor(t *testing.T) {
	logs, subject, st := committedDashboardLogs(t, []byte(strings.Repeat("fatal\n", logPageRecords+1)))
	objects := logs.objs.(blob.ObjectBackend)
	reader, _ := memberLogSearchReader(t, st, objects)
	query := controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal"}
	first, err := reader.SearchLogs(t.Context(), query)
	if err != nil || !first.Truncated {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if n, err := objects.(blob.LogDeleter).DeleteLogs(t.Context(), subject.Team, subject.RunID); err != nil || n == 0 {
		t.Fatalf("delete persisted logs: %d %v", n, err)
	}
	query.Runs.Cursor = first.NextCursor
	page, err := reader.SearchLogs(t.Context(), query)
	if err == nil || len(page.Matches) != 0 || page.Truncated || page.NextCursor != "" {
		t.Fatalf("missing bytes became a successful continuation: %+v %v", page, err)
	}
}

func logSearchCandidate(t *testing.T, st *store.Store, id string, number int, pipeline string) {
	t.Helper()
	tenant, err := st.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	request := run.Request
	request.RunID, request.Request.Pipeline = id, pipeline
	request.Request.Git = runspec.Git{Ref: "noise", SHA: "def456", Repository: runspec.Repository{Key: "gh:2", Display: "acme/other"}}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, st, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,finished_at,log_retention_class,log_retention_days) SELECT team,?,?,?,'success',?,request_hash,request_sealed_at,'gh:2',dispatch,intake_mode,allowed_pools,created_principal,created_at,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, id, number, pipeline, body)
}

func TestPersistedLogSearchAppliesRunFiltersBeforeLimit(t *testing.T) {
	logs, _, st := committedDashboardLogs(t, []byte("fatal\n"))
	tenant, err := st.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git = runspec.Git{Ref: "main", SHA: "abc123", Repository: runspec.Repository{Key: "gh:1", Display: "acme/repo"}}
	body, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	engineSQL(t, st, `UPDATE runs SET request_json=?,started_at=?,finished_at=?,error='permission denied',status='failed',lineage_json=? WHERE team='a' AND id='run'`, body, now.UnixNano(), now.UnixNano(), []byte(`{"parent_run_id":"parent"}`))
	logSearchCandidate(t, st, "newer", 2, "other")
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	for _, query := range []controllerwire.RunQuery{
		{Pipeline: "ci"},
		{Pipeline: "!other"},
		{Status: "failed"},
		{Status: "!success"},
		{Branches: []string{"other", "main", "!noise"}},
		{SHA: "AbC"},
		{SHA: "!def"},
		{Repos: []string{"acme/repo", "gh:3", "!acme/other"}},
		{Repos: []string{"gh:1"}},
		{Error: "Permission"},
		{Search: "CI PERMISSION -other"},
		{ParentRunID: "parent"},
		{StartedAfter: now, StartedBefore: now},
		{FinishedAfter: now, FinishedBefore: now},
	} {
		query.Limit = 1
		page, err := reader.SearchLogs(t.Context(), controllerwire.LogSearchQuery{Runs: query, Pattern: "fatal"})
		if err != nil || len(page.Matches) != 1 || page.Matches[0].RunID != "run" {
			t.Fatalf("filters %+v: %+v %v", query, page, err)
		}
	}
	for _, query := range []controllerwire.RunQuery{{Repos: []string{"!acme/repo"}}, {Repos: []string{"!gh:1"}}, {RootOnly: true}, {AfterNumber: 1}, {FinishedAfter: now.Add(time.Nanosecond)}} {
		query.Limit = 5
		page, err := reader.SearchLogs(t.Context(), controllerwire.LogSearchQuery{Runs: query, Pattern: "fatal"})
		if err != nil || len(page.Matches) != 0 {
			t.Fatalf("excluded %+v: %+v %v", query, page, err)
		}
	}
	engineSQL(t, st, `UPDATE runs SET error='' WHERE team='a' AND id='run'`)
	engineSQL(t, st, `UPDATE nodes SET error='job permission denied',status_detail='' WHERE team='a' AND run_id='run' AND node_id='job'`)
	engineSQL(t, st, `UPDATE node_attempts SET failure_reason='exit_nonzero' WHERE team='a' AND run_id='run' AND node_id='job'`)
	for _, query := range []controllerwire.RunQuery{{Error: "permission"}, {Search: "permission"}} {
		query.Limit = 1
		page, err := reader.SearchLogs(t.Context(), controllerwire.LogSearchQuery{Runs: query, Pattern: "fatal"})
		if err != nil || len(page.Matches) != 1 || page.Matches[0].RunID != "run" {
			t.Fatalf("masked job failure %+v: %+v %v", query, page, err)
		}
	}
}

func FuzzLogSearchCursorNeverGrantsAuthority(f *testing.F) {
	f.Add("foreign", false, uint8(0))
	f.Add("consumed", true, uint8(1))
	f.Fuzz(func(t *testing.T, noise string, revoked bool, mutation uint8) {
		logs, _, st := committedDashboardLogs(t, []byte(strings.Repeat("private\n", logPageRecords+1)))
		reader, prefix := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
		engineSQL(t, st, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
		engineSQL(t, st, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'foreign','foreign-run',3,pipeline,status,?,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, []byte(`{invalid private data`))
		query := controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "private"}
		page, err := reader.SearchLogs(t.Context(), query)
		if err != nil || !page.Truncated || len(page.Matches) != logPageRecords {
			t.Fatalf("first page: %d matches, truncated=%v: %v", len(page.Matches), page.Truncated, err)
		}
		body, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		var cursor logSearchCursor
		if err := json.Unmarshal(body, &cursor); err != nil {
			t.Fatal(err)
		}
		cursor.prepareLogs()
		digest := sha256.Sum256([]byte(noise))
		key := hex.EncodeToString(digest[:])
		cursor.Logs.Lines[key] = int64(mutation)
		cursor.Logs.Matches[key] = int64(mutation)
		if mutation%2 != 0 {
			for subject := range cursor.Logs.Lines {
				cursor.Logs.Lines[subject] = 1000000
			}
		}
		body, err = json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		query.Runs.Cursor = base64.RawURLEncoding.EncodeToString(body)
		if revoked {
			engineSQL(t, st, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, prefix)
		}
		page, err = reader.SearchLogs(t.Context(), query)
		if revoked && (err == nil || len(page.Matches) != 0) || !revoked && err != nil {
			t.Fatalf("revoked=%v: %+v %v", revoked, page, err)
		}
		for _, match := range page.Matches {
			if match.RunID != "run" || match.Subject == nil || match.Subject.Team != "a" || match.Subject.Attempt == nil || match.Subject.Attempt.NodeID != "job" || match.Line != "private" {
				t.Fatalf("cursor selected foreign bytes: %+v", match)
			}
		}
	})
}

func FuzzLogSearchCursorCannotChangeSelection(f *testing.F) {
	f.Add("noise", uint8(0))
	f.Add("team", uint8(4))
	f.Fuzz(func(t *testing.T, noise string, field uint8) {
		q := controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal"}
		cursor, err := newLogSearchCursor("a", q)
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		q.Runs.Cursor = base64.RawURLEncoding.EncodeToString(body)
		team := "a"
		switch field % 5 {
		case 0:
			q.Pattern += noise + "x"
		case 1:
			q.Runs.Branches = []string{noise + "x"}
		case 2:
			q.Job = noise + "x"
		case 3:
			q.MaxMatches = int(field) + 1
		case 4:
			team += noise + "x"
		}
		if _, err := newLogSearchCursor(team, q); err == nil {
			t.Fatal("cursor changed its selection")
		}
	})
}
