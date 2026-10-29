package controller

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLogTreeTraversesEveryTenantDescendantProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 94.3s measured in the short class; the full suite runs it")
	}
	property := func(size uint8, order uint32) bool {
		engine, _ := engineRuntimeStore(t)
		st := engine.Store
		count := 101 + int(size%7)
		want := map[string]bool{}
		for i := range count + 1 {
			id, parent := fmt.Sprintf("child-%03d", i), "run"
			if i == count {
				parent = "child-000"
			}
			want[id] = true
			logSearchCandidate(t, st, id, i+2, "ci")
			engineSQL(t, st, `UPDATE runs SET parent_run_id=?,started_at=? WHERE team='a' AND id=?`, parent, int64((uint32(i)+order)%3), id)
			engineSQL(t, st, `INSERT INTO events(team,run_id,seq,kind,ts,payload) VALUES ('a',?,1,'run_started',1,?)`, id, []byte(`{}`))
		}
		engineSQL(t, st, `UPDATE runs SET parent_run_id=? WHERE team='a' AND id='run'`, fmt.Sprintf("child-%03d", count))
		engineSQL(t, st, `INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)`)
		engineSQL(t, st, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,parent_run_id,log_retention_class,log_retention_days) SELECT 'foreign','foreign-child',1,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,'run',log_retention_class,log_retention_days FROM runs WHERE team='a' AND id='run'`)
		engineSQL(t, st, `INSERT INTO events(team,run_id,seq,kind,ts,payload) VALUES ('foreign','foreign-child',1,'run_started',1,?)`, []byte(`{"private":"foreign"}`))
		options, _, _ := memberObservationOptions(t, st, nil)
		reader, err := client.NewLogReader(options)
		if err != nil {
			t.Fatal(err)
		}
		page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Tree: true, EventsOnly: true})
		if err != nil || page.Truncated || len(page.Records) != len(want) {
			t.Logf("tree returned %d of %d descendants: %v", len(page.Records), len(want), err)
			return false
		}
		for _, record := range page.Records {
			if !want[record.RunID] {
				return false
			}
			delete(want, record.RunID)
		}
		return len(want) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistedLogPageUsesCanonicalSubjectAndCursor(t *testing.T) {
	logs, subject, st := committedDashboardLogs(t, []byte("first\nsecond\n"))
	server := New(st, nil).WithDataObjects(logs.objs.(blob.ObjectBackend))
	principal, err := st.AsOperator().ResolveAuthority(t.Context(), logs.actor.Authority)
	if err != nil {
		t.Fatal(err)
	}
	auth := capability.Authorized{Principal: principal, Binding: principal.Capability.Binding, Target: capability.Target{Team: "a", RunID: "run", NodeID: "job"}}
	raw := nativeClaimPresentation(t, newEngineFixture(st, EngineController{}), auth)
	reader, err := client.NewLogReader(client.EngineOptions{URL: "http://local", HTTP: &http.Client{Transport: localHostRoundTripper{server.Handler()}}, Bearer: func(context.Context) (string, error) { return raw, nil }})
	if err != nil {
		t.Fatal(err)
	}
	read := func(cursor string) controllerwire.LogPage {
		t.Helper()
		page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Job: "job", NoEvents: true, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := read("")
	if len(page.Records) != 2 || page.Records[0].Subject == nil || page.Records[0].Subject.Attempt == nil || *page.Records[0].Subject.Attempt != *subject.Attempt || page.Records[0].Line != "first" || page.Records[1].LineNo != 2 {
		t.Fatalf("canonical records: %+v", page)
	}
	if page.Complete {
		t.Fatal("a sealed attempt made an unfinished run complete")
	}
	if idle := read(page.NextCursor); len(idle.Records) != 0 || idle.NextCursor != page.NextCursor || idle.Complete {
		t.Fatalf("idle poll repeated output or ended early: %+v", idle)
	}
	if strings.Contains(page.NextCursor, "first") || strings.Contains(page.NextCursor, "second") {
		t.Fatal("cursor retained log payload")
	}
	numeric, err := reader.ReadLogs(t.Context(), "1", controllerwire.LogQuery{Job: "job", NoEvents: true})
	if err != nil || !reflect.DeepEqual(numeric, page) {
		t.Fatalf("numeric logs changed canonical subject or cursor: %+v %v", numeric, err)
	}
}

func TestPersistedLogPageSelectionsAndContinuation(t *testing.T) {
	var body strings.Builder
	for i := 1; i <= logPageRecords+7; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	logs, subject, _ := committedDashboardLogs(t, []byte(body.String()))
	for _, test := range []struct {
		name string
		q    controllerwire.LogQuery
		want []int64
	}{
		{"head", controllerwire.LogQuery{Head: new(2)}, []int64{1, 2}},
		{"tail", controllerwire.LogQuery{Tail: new(2)}, []int64{506, 507}},
		{"tail on later subject", controllerwire.LogQuery{Tail: new(2), Cursor: "previous-subject"}, []int64{506, 507}},
		{"zero tail", controllerwire.LogQuery{Tail: new(0)}, nil},
		{"range", controllerwire.LogQuery{Lines: "3:4"}, []int64{3, 4}},
	} {
		t.Run(test.name, func(t *testing.T) {
			cursor, err := newLogPageCursor("", "run", "a", test.q)
			if err != nil {
				t.Fatal(err)
			}
			page := controllerwire.LogPage{Complete: true}
			if err := appendSubjectPage(t.Context(), logs, subject, test.q, nil, &cursor, &page); err != nil {
				t.Fatal(err)
			}
			var numbers []int64
			for _, record := range page.Records {
				numbers = append(numbers, record.LineNo)
			}
			if !reflect.DeepEqual(numbers, test.want) || !page.Complete || page.Truncated {
				t.Fatalf("selection returned %d records, complete=%v truncated=%v, want %v", len(page.Records), page.Complete, page.Truncated, test.want)
			}
		})
	}
	cursor, _ := newLogPageCursor("", "run", "a", controllerwire.LogQuery{})
	page := controllerwire.LogPage{Complete: true}
	if err := appendSubjectPage(t.Context(), logs, subject, controllerwire.LogQuery{}, nil, &cursor, &page); err != nil || !page.Truncated || len(page.Records) != logPageRecords {
		t.Fatalf("first page = %+v: %v", page, err)
	}
	page = controllerwire.LogPage{Complete: true}
	if err := appendSubjectPage(t.Context(), logs, subject, controllerwire.LogQuery{}, nil, &cursor, &page); err != nil || page.Truncated || len(page.Records) != 7 || page.Records[0].LineNo != 501 {
		t.Fatalf("continuation = %+v: %v", page, err)
	}
}

func FuzzLogPageCursorCannotChangeSelection(f *testing.F) {
	f.Add("foreign", uint8(0))
	f.Add("other", uint8(3))
	f.Fuzz(func(t *testing.T, value string, field uint8) {
		q := controllerwire.LogQuery{Job: "job", NoEvents: true}
		cursor, _ := newLogPageCursor("", "run", "team", q)
		body, _ := json.Marshal(cursor)
		raw := base64.RawURLEncoding.EncodeToString(body)
		run, team := "run", "team"
		switch field % 4 {
		case 0:
			run += value + "x"
		case 1:
			team += value + "x"
		case 2:
			q.Job += value + "x"
		case 3:
			q.NoEvents = false
		}
		if _, err := newLogPageCursor(raw, run, team, q); err == nil {
			t.Fatal("cursor changed its selection")
		}
	})
}

func TestPersistedLogPageRechecksAuthorityDespiteConsumedCursor(t *testing.T) {
	logs, subject, st := committedDashboardLogs(t, []byte("private\n"))
	q := controllerwire.LogQuery{NoEvents: true}
	cursor, _ := newLogPageCursor("", "run", "a", q)
	if err := appendSubjectPage(t.Context(), logs, subject, q, nil, &cursor, &controllerwire.LogPage{}); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, st, "UPDATE capabilities SET revoked_at=1 WHERE prefix=?", logs.actor.CapabilityPrefix)
	page := controllerwire.LogPage{}
	if err := appendSubjectPage(t.Context(), logs, subject, q, nil, &cursor, &page); err == nil || len(page.Records) != 0 {
		t.Fatal("consumed cursor bypassed current authority")
	}
}

func TestPersistedLogPageTimestampFilterAndMissingSegments(t *testing.T) {
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	logs, subject, _ := committedDashboardLogs(t, []byte(fmt.Sprintf("{\"ts\":%q,\"msg\":\"old fatal\"}\n{\"ts\":%q,\"msg\":\"new fatal\"}\n", old, now)))
	q := controllerwire.LogQuery{Since: time.Minute, Grep: "fatal"}
	cursor, err := newLogPageCursor("", "run", "a", q)
	if err != nil {
		t.Fatal(err)
	}
	page := controllerwire.LogPage{Complete: true}
	if err := appendSubjectPage(t.Context(), logs, subject, q, regexp.MustCompile(q.Grep), &cursor, &page); err != nil || len(page.Records) != 1 || page.Records[0].LineNo != 2 || page.Records[0].Time == nil {
		t.Fatalf("timestamp selection = %+v: %v", page, err)
	}
	logs.objs = newScriptedSegmentLogs()
	cursor, err = newLogPageCursor("", "run", "a", q)
	if err != nil {
		t.Fatal(err)
	}
	page = controllerwire.LogPage{Complete: true}
	if err := appendSubjectPage(t.Context(), logs, subject, q, nil, &cursor, &page); err == nil || len(page.Records) > 0 {
		t.Fatal("missing committed bytes became a successful empty poll")
	}
}

func TestLogPageQueryRejectsInvalidSelections(t *testing.T) {
	for _, query := range []string{"tail=-1", "tail=x", "tail=0&head=0", "lines=0:1", "lines=2:1", "since=garbage", "since=-1s", "tree=garbage", "events_only=true&no_events=true"} {
		request := httptest.NewRequest(http.MethodGet, "/logs?"+query, nil)
		if _, err := parseLogPageQuery(request, "job"); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
}

func TestPersistedLogPagesRetainOwnerUnitIdentity(t *testing.T) {
	for _, kind := range []blob.LogUnitKind{blob.LogTrigger, blob.LogPlan, blob.LogEval} {
		t.Run(string(kind), func(t *testing.T) {
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "team", RunID: "run", Unit: &blob.LogUnit{Kind: kind, Generation: 7}}
			if kind == blob.LogEval {
				subject.Unit.Source = &blob.LogSource{NodeID: "source", Generation: 3}
			}
			fixture := newScriptedSegmentLogs()
			fixture.add(t, subject, "owner output\n", true)
			cursor, _ := newLogPageCursor("", "run", "team", controllerwire.LogQuery{})
			page := controllerwire.LogPage{Complete: true}
			if err := appendSubjectPage(t.Context(), fixture.reader(), subject, controllerwire.LogQuery{}, nil, &cursor, &page); err != nil || len(page.Records) != 1 {
				t.Fatalf("owner page: %+v %v", page, err)
			}
			record := page.Records[0]
			if err := record.Validate(); err != nil || record.Subject.Attempt != nil || !reflect.DeepEqual(record.Subject.Unit, subject.Unit) || record.JobID != "" {
				t.Fatalf("owner unit became a job attempt: %+v %v", record, err)
			}
		})
	}
}

func FuzzPersistedLogPageCursorCannotReplaceSubjectAuthority(f *testing.F) {
	f.Add(uint8(5), "current", false)
	f.Add(uint8(0), "foreign", false)
	f.Add(uint8(5), "revoked", true)
	f.Fuzz(func(t *testing.T, field uint8, noise string, revoked bool) {
		logs, subject, st := committedDashboardLogs(t, []byte("private\n"))
		suffix := fmt.Sprintf("%x", sha256.Sum256([]byte(noise)))
		switch field % 6 {
		case 0:
			subject.Team += suffix
			subject.Attempt.Team = subject.Team
		case 1:
			subject.RunID += suffix
			subject.Attempt.RunID = subject.RunID
		case 2:
			subject.Attempt.NodeID += suffix
		case 3:
			subject.Attempt.Generation++
		case 4:
			subject.Attempt.Attempt++
		}
		if revoked {
			engineSQL(t, st, "UPDATE capabilities SET revoked_at=1 WHERE prefix=?", logs.actor.CapabilityPrefix)
		}
		cursor, _ := newLogPageCursor("", subject.RunID, subject.Team, controllerwire.LogQuery{})
		key, _ := json.Marshal(subject)
		cursor.Lines[string(key)], cursor.Offsets = 1000000, map[string]int64{string(key): 1000000}
		page := controllerwire.LogPage{}
		err := appendSubjectPage(t.Context(), logs, subject, controllerwire.LogQuery{}, nil, &cursor, &page)
		if (err == nil) != (!revoked && field%6 == 5) || len(page.Records) != 0 {
			t.Fatalf("cursor substituted for exact current subject authority: %+v %v", page, err)
		}
	})
}

func appendSubjectPage(ctx context.Context, reader segmentLogs, subject blob.LogSubject, q controllerwire.LogQuery, pattern *regexp.Regexp, cursor *logPageCursor, page *controllerwire.LogPage) error {
	budget, cancel := newLogBudget(ctx)
	defer cancel()
	return appendSubjectLines(reader, subject, q, 0, pattern, cursor, page, budget)
}
