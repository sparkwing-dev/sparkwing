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
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/quicktest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRunLogPageAccountsForRequeuedPlanWriters(t *testing.T) {
	for _, test := range []struct {
		name, status string
		exchanged    bool
	}{
		{"bootstrap success", "success", false},
		{"bootstrap failure", "failed", false},
		{"lost writer", "success", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs, _, st := committedDashboardLogs(t, []byte("job output\n"))
			tenant, err := st.ForTeam(t.Context(), "a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "plan-logs", CreatedBy: "fixture"}); err != nil {
				t.Fatal(err)
			}
			engineHTTPRoot(t, &engineFixture{Store: st}, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
			engineSQL(t, st, `UPDATE runs SET dispatch='controller'`)
			engineSQL(t, st, `UPDATE triggers SET unit_kind='plan'`)
			demand := store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: "run"}}
			for cycle := range 3 {
				claim, err := st.AsOperator().ClaimUnit(t.Context(), demand)
				if err != nil {
					t.Fatal(err)
				}
				if test.exchanged {
					issued, err := st.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
					if err != nil {
						t.Fatal(err)
					}
					claim.Award.CapabilityPrefix = issued.Capability.Prefix
				}
				if cycle == 1 {
					engineSQL(t, st, `UPDATE triggers SET lease_expires_at=?`, teststore.Now(st).UnixNano())
					reaped, err := st.AsOperator().ReapUnits(t.Context(), 1)
					if err != nil || len(reaped) != 1 || !reaped[0].Requeued {
						t.Fatalf("expired plan: %+v %v", reaped, err)
					}
				} else if err := st.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
					t.Fatal(err)
				}
			}
			claim, err := st.AsOperator().ClaimUnit(t.Context(), demand)
			if err != nil {
				t.Fatal(err)
			}
			issued, err := st.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
			if err != nil {
				t.Fatal(err)
			}
			principal, err := st.AsOperator().ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix})
			if err != nil {
				t.Fatal(err)
			}
			subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: int64(claim.Award.Generation)}}
			engineSQL(t, st, `UPDATE runs SET status=?,finished_at=?`, test.status, teststore.Now(st).UnixNano())
			reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
			jobPage, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Job: "job", NoEvents: true})
			if err != nil || !jobPage.Complete {
				t.Fatalf("job logs: %+v %v", jobPage, err)
			}
			page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{NoEvents: true})
			if err != nil || page.Complete {
				t.Fatalf("unsealed owner was hidden: complete=%v error=%v", page.Complete, err)
			}
			if err := tenant.RecordSegments(t.Context(), store.SegmentCommit{Actor: engineActor(capability.Authorized{Principal: principal, Binding: principal.Capability.Binding}), Subject: subject, Final: true, RetentionClass: "r30"}); err != nil {
				t.Fatal(err)
			}
			page, err = reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{NoEvents: true})
			if err != nil || page.Complete != !test.exchanged || len(page.Records) != 1 || page.Records[0].Line != "job output" {
				t.Fatalf("finished run aggregate: complete=%v records=%v error=%v", page.Complete, page.Records, err)
			}
		})
	}
}

func TestLogTreeTraversesEveryTenantDescendantProperty(t *testing.T) {
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
	if err := quick.Check(property, quicktest.Config(&quick.Config{MaxCount: 8})); err != nil {
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

func TestPersistedLogByteCapNamesTheTruncationLimit(t *testing.T) {
	logs, _, st := committedDashboardLogs(t, []byte("last retained line\n[sparkwing] truncated: the node log reached its 64 MiB cap\n"))
	engineSQL(t, st, `UPDATE log_streams SET completeness='incomplete',incomplete_reason='byte cap' WHERE team='a' AND run_id='run'`)
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	_, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Job: "job", NoEvents: true})
	if err == nil || !strings.Contains(err.Error(), "truncated") || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("missing public truncation limit: %v", err)
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

func TestPersistedLogPageEndsUnsealedTerminalObservationAfterGrace(t *testing.T) {
	for _, kind := range []string{"job_attempt", "owner_unit"} {
		for _, test := range []struct {
			name                         string
			age                          time.Duration
			terminal, sealed, incomplete bool
		}{
			{"running", 2 * limits.LogSealGrace, false, false, false},
			{"tail grace", limits.LogSealGrace - time.Nanosecond, true, false, false},
			{"exact grace", limits.LogSealGrace, true, false, true},
			{"abandoned", 2 * limits.LogSealGrace, true, false, true},
			{"sealed", 2 * limits.LogSealGrace, true, true, false},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				logs, _, st := committedDashboardLogs(t, []byte("tail\n"))
				now := teststore.Now(st)
				if kind == "owner_unit" {
					completeness := "open"
					if test.sealed {
						completeness = "complete"
					}
					owner := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 1}}
					encoded, err := json.Marshal(owner)
					if err != nil {
						t.Fatal(err)
					}
					engineSQL(t, st, `INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,prefix,retention_class,completeness,created_at,updated_at) VALUES ('a','run',?,'owner_unit',1,'trigger','fixture','r30',?,1,1)`, fmt.Sprintf("%x", sha256.Sum256(encoded)), completeness)
				} else if !test.sealed {
					engineSQL(t, st, `UPDATE log_streams SET completeness='open',sealed_at=NULL WHERE team='a' AND run_id='run'`)
				}
				if test.terminal {
					engineSQL(t, st, `UPDATE runs SET status='failed',finished_at=? WHERE team='a' AND id='run'`, now.Add(-test.age).UnixNano())
				}
				reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
				page, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{NoEvents: true})
				if test.incomplete {
					if err != nil || len(page.Records) != 1 || page.Complete {
						t.Fatalf("abandoned logs lost their committed tail: %+v %v", page, err)
					}
					page, err = reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{NoEvents: true, Cursor: page.NextCursor})
					if err == nil || !strings.Contains(err.Error(), "logs incomplete: the runner never sealed this log") {
						t.Fatalf("abandoned logs kept observation open: %+v %v", page, err)
					}
					return
				}
				if err != nil || page.Complete != (test.terminal && test.sealed) || len(page.Records) != 1 {
					t.Fatalf("grace/seal verdict: %+v %v", page, err)
				}
			})
		}
	}
}
