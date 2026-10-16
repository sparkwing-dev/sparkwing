package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestPersistedRunPagesUsePublicDetailsAndStableNumberBoundary(t *testing.T) {
	logs, subject, st := committedDashboardLogs(t, []byte("output\n"))
	logSearchCandidate(t, st, "second", 2, "ci")
	logSearchCandidate(t, st, "third", 3, "ci")
	options, _, _ := memberObservationOptions(t, st, logs.objs.(blob.ObjectBackend))
	reader, err := client.NewRunReader(options)
	if err != nil {
		t.Fatal(err)
	}
	query := controllerwire.RunQuery{Limit: 1}
	page, err := reader.ListRuns(t.Context(), query)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "third" || page.Runs[0].Number != 3 || page.Runs[0].Inputs.Pipeline != "ci" || !page.Truncated || page.NextCursor == "" {
		t.Fatalf("public first page: %+v %v", page, err)
	}
	logSearchCandidate(t, st, "later", 4, "ci")
	query.Cursor = page.NextCursor
	page, err = reader.ListRuns(t.Context(), query)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "second" || !page.Truncated || page.NextCursor == "" || page.NextCursor == query.Cursor {
		t.Fatalf("stable second page: %+v %v", page, err)
	}
	query.Cursor = page.NextCursor
	page, err = reader.ListRuns(t.Context(), query)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "run" || page.Truncated || page.NextCursor != "" {
		t.Fatalf("final page: %+v %v", page, err)
	}
	detail, err := reader.ReadRun(t.Context(), "run")
	if err != nil || !reflect.DeepEqual(page.Runs[0], detail) || len(detail.Logs) != 1 || !reflect.DeepEqual(detail.Logs[0], subject) || len(detail.Attempts) != 1 {
		t.Fatalf("list and status differ: %+v %+v %v", page.Runs[0], detail, err)
	}
	query = controllerwire.RunQuery{Limit: 10, AfterNumber: 2}
	page, err = reader.ListRuns(t.Context(), query)
	if err != nil || len(page.Runs) != 2 || page.Runs[0].ID != "later" || page.Runs[1].ID != "third" || page.Truncated {
		t.Fatalf("exclusive lower bound: %+v %v", page, err)
	}
}

func TestPersistedRunPagesFilterBeforeLimitAndPreserveCurrentNotices(t *testing.T) {
	server, tenant := engineRuntimeStore(t)
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Git = runspec.Git{Ref: "main", SHA: "abc123", Repository: runspec.Repository{Key: "gh:1", Display: "acme/repo"}}
	run.Request.Request.RequestedArgs = map[string]string{"private": "private-value"}
	body, err := json.Marshal(run.Request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	engineSQL(t, server.Store, `UPDATE runs SET request_json=?,started_at=?,finished_at=?,error='masked permission denied',status='failed',lineage_json=? WHERE team='a' AND id='run'`, body, now.UnixNano(), now.UnixNano(), []byte(`{"parent_run_id":"parent"}`))
	logSearchCandidate(t, server.Store, "other", 2, "other")
	options, _, api := memberObservationOptions(t, server.Store, nil)
	reader, err := client.NewRunReader(options)
	if err != nil {
		t.Fatal(err)
	}
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
		page, err := reader.ListRuns(t.Context(), query)
		if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "run" || page.Truncated {
			t.Fatalf("filtered %+v: %+v %v", query, page, err)
		}
		body, err := json.Marshal(page)
		if err != nil || bytes.Contains(body, []byte("private-value")) || bytes.Contains(body, []byte("request_hash")) {
			t.Fatalf("private inputs in public page: %s %v", body, err)
		}
	}
	for _, query := range []controllerwire.RunQuery{{RootOnly: true, Pipeline: "ci"}, {FinishedAfter: now.Add(time.Nanosecond), Pipeline: "ci"}, {Branches: []string{"!main", "!noise"}}} {
		query.Limit = 5
		page, err := reader.ListRuns(t.Context(), query)
		if err != nil || page.Runs == nil || len(page.Runs) != 0 || page.Truncated || page.NextCursor != "" {
			t.Fatalf("empty %+v: %+v %v", query, page, err)
		}
	}
	spec, err := json.Marshal(runspec.Spec{Request: run.Request, Resolved: runspec.Resolved{Engine: engine.Identity{Version: "v1.2.3"}}})
	if err != nil {
		t.Fatal(err)
	}
	engineSQL(t, server.Store, `UPDATE runs SET spec_json=?,spec_hash='spec',sealed_at=1,plan_json=?,plan_hash='plan' WHERE team='a' AND id='run'`, spec, []byte(`{"jobs":[]}`))
	policy := &engineVersionFake{notices: []engine.Notice{{Kind: engine.NoticeSupportEnding, Version: "v1.2.3", Reason: "signed warning"}}}
	api.WithEngineController(&EngineController{Store: server.Store, VersionPolicy: engineVersionPolicy(policy)})
	page, err := reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 1, Pipeline: "ci"})
	if err != nil || len(page.Runs) != 1 || !reflect.DeepEqual(page.Runs[0].Notices, policy.notices) {
		t.Fatalf("read-time notice: %+v %v", page, err)
	}
	policy.indexErr = invalidEngineBody("policy failed")
	page, err = reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 10})
	if err == nil || len(page.Runs) != 0 || page.NextCursor != "" {
		t.Fatalf("failed policy returned partial page: %+v %v", page, err)
	}
}

func TestPersistedRunPagesScanPastInternalBatchesWithoutCappingLimit(t *testing.T) {
	server, _ := engineRuntimeStore(t)
	for number := 2; number <= 105; number++ {
		pipeline := "noise"
		if number == 2 {
			pipeline = "ci"
		}
		logSearchCandidate(t, server.Store, "candidate-"+strconv.Itoa(number), number, pipeline)
	}
	options, _, _ := memberObservationOptions(t, server.Store, nil)
	reader, err := client.NewRunReader(options)
	if err != nil {
		t.Fatal(err)
	}
	query := controllerwire.RunQuery{Limit: 1, Pipeline: "ci"}
	page, err := reader.ListRuns(t.Context(), query)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Number != 2 || !page.Truncated {
		t.Fatalf("matched beyond batch boundary: %+v %v", page, err)
	}
	query.Cursor = page.NextCursor
	page, err = reader.ListRuns(t.Context(), query)
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Number != 1 || page.Truncated {
		t.Fatalf("oldest match: %+v %v", page, err)
	}
	page, err = reader.ListRuns(t.Context(), controllerwire.RunQuery{Limit: 200})
	if err != nil || len(page.Runs) != 105 || page.Truncated {
		t.Fatalf("uncapped page: %d runs, truncated=%v %v", len(page.Runs), page.Truncated, err)
	}
	for i, run := range page.Runs {
		if run.Number != int64(105-i) {
			t.Fatalf("number order at %d: %+v", i, run)
		}
	}
}

func FuzzRunPageCursorNeverGrantsAuthorityOrDisclosesPrivateInputs(f *testing.F) {
	f.Add("private", true, uint8(0))
	f.Add("retained", false, uint8(1))
	f.Add("value", false, uint8(3))
	f.Fuzz(func(t *testing.T, noise string, revoke bool, mutation uint8) {
		server, tenant := engineRuntimeStore(t)
		logSearchCandidate(t, server.Store, "second", 2, "ci")
		private := "private-value-" + hex.EncodeToString([]byte(noise))
		run, err := tenant.ReadRun(t.Context(), "run")
		if err != nil {
			t.Fatal(err)
		}
		run.Request.Request.RequestedArgs = map[string]string{"secret": private}
		body, err := json.Marshal(run.Request)
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, server.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, body)
		options, prefix, _ := memberObservationOptions(t, server.Store, nil)
		reader, err := client.NewRunReader(options)
		if err != nil {
			t.Fatal(err)
		}
		query := controllerwire.RunQuery{Limit: 1}
		page, err := reader.ListRuns(t.Context(), query)
		if err != nil || !page.Truncated {
			t.Fatalf("first page: %+v %v", page, err)
		}
		engineSQL(t, server.Store, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
		engineSQL(t, server.Store, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) SELECT 'foreign','foreign-run',3,pipeline,status,?,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`, []byte(`{invalid private data`))
		body, err = base64.RawURLEncoding.DecodeString(page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		var cursor map[string]any
		if err := json.Unmarshal(body, &cursor); err != nil {
			t.Fatal(err)
		}
		cursor["unknown"] = noise
		cursor["through"] = int(mutation)
		body, err = json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		query.Cursor = base64.RawURLEncoding.EncodeToString(body)
		if revoke {
			engineSQL(t, server.Store, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, prefix)
		}
		page, err = reader.ListRuns(t.Context(), query)
		if revoke && (err == nil || len(page.Runs) != 0) {
			t.Fatalf("revoked cursor read: %+v %v", page, err)
		}
		for _, run := range page.Runs {
			if run.Team != "a" || run.ID != "run" && run.ID != "second" || strings.Contains(run.ID, "foreign") {
				t.Fatalf("cursor escaped tenant: %+v", run)
			}
		}
		body, err = json.Marshal(page)
		if err != nil || bytes.Contains(body, []byte(private)) {
			t.Fatalf("page disclosed private inputs: %s %v", body, err)
		}
	})
}

func FuzzRunPageCursorCannotChangeSelection(f *testing.F) {
	f.Add("different", uint8(0))
	f.Add("team", uint8(4))
	f.Fuzz(func(t *testing.T, noise string, field uint8) {
		query := controllerwire.RunQuery{Limit: 1}
		cursor, err := newRunPageCursor("a", query)
		if err != nil {
			t.Fatal(err)
		}
		cursor.Snapshot, cursor.Through = 2, 1
		body, err := json.Marshal(cursor)
		if err != nil {
			t.Fatal(err)
		}
		query.Cursor = base64.RawURLEncoding.EncodeToString(body)
		team := "a"
		switch field % 7 {
		case 0:
			query.Pipeline = noise + "x"
		case 1:
			query.Branches = []string{noise + "x"}
		case 2:
			query.AfterNumber = int64(field) + 1
		case 3:
			query.Limit++
		case 4:
			team += noise + "x"
		case 5:
			query.ParentRunID = noise + "x"
		case 6:
			query.FinishedBefore = time.Unix(int64(field), 0)
		}
		if _, err := newRunPageCursor(team, query); err == nil {
			t.Fatal("cursor changed its selection")
		}
	})
}
