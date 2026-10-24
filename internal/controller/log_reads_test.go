package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type scriptedSegmentLogs struct {
	view        store.RunView
	actor       runstate.Actor
	views       map[string]runstate.LogSegments
	objects     map[string][]byte
	denied      bool
	reads, gets int
	onRead      func(blob.LogReadRequest)
}

func (f *scriptedSegmentLogs) ReadRunView(context.Context, string) (store.RunView, error) {
	return f.view, nil
}

func logSubjectID(subject blob.LogSubject) string {
	body, _ := json.Marshal(subject)
	return string(body)
}

func (f *scriptedSegmentLogs) ReadLogSegments(_ context.Context, actor runstate.Actor, request blob.LogReadRequest) (runstate.LogSegments, error) {
	f.reads++
	if f.onRead != nil {
		f.onRead(request)
	}
	if f.denied || actor != f.actor {
		return runstate.LogSegments{}, errors.New("reader authority ended")
	}
	view, ok := f.views[logSubjectID(request.Subject)]
	if !ok {
		return runstate.LogSegments{}, store.ErrNotFound
	}
	view.Segments = nil
	for _, segment := range f.views[logSubjectID(request.Subject)].Segments {
		if segment.Offset+segment.Size > request.Offset {
			view.Segments = append(view.Segments, segment)
		}
	}
	return view, nil
}

func (f *scriptedSegmentLogs) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.gets++
	data, ok := f.objects[key]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func newScriptedSegmentLogs() *scriptedSegmentLogs {
	return &scriptedSegmentLogs{actor: runstate.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthoritySession, ID: "reader"}}, views: map[string]runstate.LogSegments{}, objects: map[string][]byte{}}
}

func (f *scriptedSegmentLogs) reader() segmentLogs {
	return segmentLogs{records: f, actor: f.actor, objs: f}
}

func (f *scriptedSegmentLogs) add(t *testing.T, subject blob.LogSubject, body string, complete bool) {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	key, err := blob.LogObjectKey(subject, "r30", 1)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(compressed.Bytes())
	f.objects[key] = compressed.Bytes()
	f.views[logSubjectID(subject)] = runstate.LogSegments{Subject: subject, Complete: complete, Segments: []runstate.Segment{{Index: 1, Offset: 0, Size: int64(len(body)), StoredBytes: int64(compressed.Len()), Digest: hex.EncodeToString(sum[:]), ObjectKey: key}}}
	f.view.Logs = append(f.view.Logs, subject)
}

func jobLogSubject(attempt, generation int64) blob.LogSubject {
	return blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: attempt, Generation: generation}}
}

func TestCommittedDashboardLogReadsTaggedInventory(t *testing.T) {
	f := newScriptedSegmentLogs()
	f.add(t, jobLogSubject(2, 3), "latest\nneedle\n", true)
	f.add(t, jobLogSubject(1, 2), "first\n", true)
	f.add(t, blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}, "planning\n", true)
	f.add(t, blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 2, Source: &blob.LogSource{NodeID: "job", Generation: 3}}}, "evaluating\n", true)
	f.objects["logs/r30/uncommitted"] = []byte("uncommitted data")
	reader := f.reader()
	body, err := reader.Read(t.Context(), "run", "job", storage.ReadOpts{Grep: "needle"})
	if err != nil || string(body) != "needle\n" {
		t.Fatalf("latest/filter=%q: %v", body, err)
	}
	all, err := reader.ReadRun(t.Context(), "run")
	if err != nil || !strings.Contains(string(all), "=== plan ===\nplanning\n") || !strings.Contains(string(all), "=== eval/job ===\nevaluating\n") || strings.Contains(string(all), "uncommitted") {
		t.Fatalf("run log=%q: %v", all, err)
	}
	if empty, err := reader.Read(t.Context(), "run", "missing", storage.ReadOpts{}); err != nil || len(empty) != 0 {
		t.Fatalf("missing log=%q: %v", empty, err)
	}
	verdict, found, err := reader.NodeLogCompleteness(t.Context(), "run", "job")
	if err != nil || !found || verdict.State != logseg.StateComplete {
		t.Fatalf("complete=%+v,%v: %v", verdict, found, err)
	}
}

func TestCommittedDashboardLogAuthorityProperty(t *testing.T) {
	property := func(body string, mode uint8) bool {
		body = "line:" + body + "\n"
		f := newScriptedSegmentLogs()
		subject := jobLogSubject(1, 1)
		f.add(t, subject, body, true)
		reader := f.reader()
		switch mode % 3 {
		case 0:
			reader.actor.Authority.ID = "foreign-" + body
		case 1:
			f.onRead = func(blob.LogReadRequest) { f.denied = true }
		case 2:
			if got, err := reader.Read(t.Context(), "run", "job", storage.ReadOpts{}); err != nil || string(got) != body {
				t.Logf("valid read=%q: %v", got, err)
				return false
			}
			f.denied = true
		}
		before := f.gets
		got, err := reader.Read(t.Context(), "run", "job", storage.ReadOpts{})
		if err == nil || len(got) != 0 || f.gets != before {
			t.Logf("refused read=%q gets=%d->%d: %v", got, before, f.gets, err)
			return false
		}
		got, err = reader.ReadRun(t.Context(), "run")
		return err != nil && len(got) == 0 && f.gets == before
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100, Rand: rand.New(rand.NewSource(318))}); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedDashboardLogCorruptionAndIncomplete(t *testing.T) {
	for _, mode := range []string{"corrupt", "missing", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			f := newScriptedSegmentLogs()
			subject := jobLogSubject(1, 1)
			f.add(t, subject, "line\n", true)
			key, _ := blob.LogObjectKey(subject, "r30", 1)
			switch mode {
			case "corrupt":
				f.objects[key] = []byte("wrong bytes")
			case "missing":
				delete(f.objects, key)
			case "incomplete":
				view := f.views[logSubjectID(subject)]
				view.Complete = false
				view.IncompleteReason = "writer_lost"
				f.views[logSubjectID(subject)] = view
			}
			body, err := f.reader().Read(t.Context(), "run", "job", storage.ReadOpts{})
			if mode != "incomplete" {
				if err == nil || len(body) != 0 {
					t.Fatalf("bad bytes=%q: %v", body, err)
				}
				return
			}
			if err != nil || string(body) != "line\n[logs incomplete: writer_lost]\n" {
				t.Fatalf("incomplete=%q: %v", body, err)
			}
			var stream bytes.Buffer
			if err := f.reader().streamSegments(t.Context(), subject, 0, &stream, func(off int64) string { return fmt.Sprint(off) }); err != nil || !strings.Contains(stream.String(), "event: log_incomplete") {
				t.Fatalf("incomplete stream=%q: %v", stream.String(), err)
			}
		})
	}
}

func TestCommittedDashboardLogResumeUsesAttemptOrder(t *testing.T) {
	f := newScriptedSegmentLogs()
	f.add(t, jobLogSubject(2, 4), "second\n", true)
	f.add(t, jobLogSubject(1, 3), "first\n", true)
	now := time.Unix(100, 0)
	f.view.Snapshot = store.RunSnapshot{Run: store.RunRow{Execution: runstate.Execution{FinishedAt: &now}}, Nodes: []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Status: runstate.StatusDone, Outcome: runstate.NodeSuccess}}}
	reader := f.reader()
	var out bytes.Buffer
	if err := reader.streamNode(t.Context(), "run", "job", logCursor{attempt: 0, off: 3}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "data: st\n") || strings.Contains(out.String(), "data: first\n") || !strings.Contains(out.String(), "data: second\n") || !strings.Contains(out.String(), "event: stream_end") {
		t.Fatalf("resumed=%q", out.String())
	}
	if _, err := parseLogCursor((logCursor{attempt: 1, off: 7}).encode("run", "job"), "other", "job"); !errors.Is(err, storage.ErrBadCursor) {
		t.Fatalf("cross-run cursor=%v", err)
	}
}

func TestCommittedDashboardLogUsesFinalStore(t *testing.T) {
	engine, tenant := engineRuntimeStore(t)
	authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
	actor := engineActor(authorized)
	ref := runspec.NodeRef{RunID: "run", NodeID: "job"}
	ordinal := int64(0)
	engineAdmitExecution(t, engine.Store, authorized.Principal)
	started, err := tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: (*runspec.AttemptOrdinal)(&ordinal)})
	if err != nil {
		t.Fatal(err)
	}
	subject := jobLogSubject(int64(started.Attempt), int64(started.Generation))
	objects := newScriptedSegmentLogs()
	objects.add(t, subject, "persisted log\n", true)
	commit := store.SegmentCommit{Subject: subject, Actor: actor, Segments: objects.views[logSubjectID(subject)].Segments, Final: true, RetentionClass: "r30"}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	ordinal = int64(started.Attempt)
	if _, err := tenant.TransitionNode(t.Context(), store.ReportNode{Ref: ref, Actor: actor, SourceAttempt: (*runspec.AttemptOrdinal)(&ordinal), Outcome: runstate.NodeSuccess, Result: &runstate.NodeResult{Outcome: runstate.NodeSuccess}}); err != nil {
		t.Fatal(err)
	}
	reader := segmentLogs{records: tenant, actor: actor, objs: objects}
	body, err := reader.Read(t.Context(), "run", "job", storage.ReadOpts{})
	if err != nil || string(body) != "persisted log\n" {
		t.Fatalf("final-schema read=%q: %v", body, err)
	}
	verdict, found, err := reader.NodeLogCompleteness(t.Context(), "run", "job")
	if err != nil || !found || verdict.State != logseg.StateComplete {
		t.Fatalf("final-schema seal=%+v,%v: %v", verdict, found, err)
	}
	engineSQL(t, engine.Store, `UPDATE capabilities SET revoked_at=? WHERE prefix=?`, time.Now().Add(-time.Second).UnixNano(), authorized.Principal.Capability.Prefix)
	before := objects.gets
	if body, err := reader.Read(t.Context(), "run", "job", storage.ReadOpts{}); err == nil || len(body) != 0 || objects.gets != before {
		t.Fatalf("revoked final-schema read=%q: %v", body, err)
	}
}

type liveTailWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *liveTailWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.cancel()
	return n, err
}

func TestCommittedDashboardLiveTailRechecksAuthority(t *testing.T) {
	for _, denied := range []bool{false, true} {
		f := newScriptedSegmentLogs()
		subject := jobLogSubject(1, 3)
		f.add(t, subject, "durable\n", false)
		f.denied = denied
		reader := f.reader()
		reader.live = newLiveLogs()
		reader.live.appendAt("run", "job", store.LiveLogAttempt(1, 3), 0, []byte("live tail\n"))
		ctx, cancel := context.WithCancel(t.Context())
		out := &liveTailWriter{cancel: cancel}
		off := int64(0)
		err := reader.tailLiveRing(ctx, subject, &off, out, func(off int64) string { return fmt.Sprint(off) })
		cancel()
		if denied {
			if err == nil || out.Len() != 0 || off != 0 {
				t.Fatalf("denied live bytes=%q offset=%d: %v", out.String(), off, err)
			}
		} else if !errors.Is(err, context.Canceled) || off != 10 || !strings.Contains(out.String(), "data: live tail\n") {
			t.Fatalf("live tail=%q offset=%d: %v", out.String(), off, err)
		}
		if f.reads != 1 {
			t.Fatalf("authority reads=%d", f.reads)
		}
	}
}

func TestCommittedDashboardUnsealedLogGrace(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		name     string
		age      time.Duration
		finished bool
		state    string
	}{
		{"running", 0, false, logseg.StateStreaming},
		{"tail grace", limits.LogSealGrace - time.Nanosecond, true, logseg.StateStreaming},
		{"exact grace", limits.LogSealGrace, true, logseg.StateIncomplete},
		{"abandoned", 2 * limits.LogSealGrace, true, logseg.StateIncomplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			records := newScriptedSegmentLogs()
			subject := jobLogSubject(1, 1)
			records.add(t, subject, "tail\n", false)
			records.view.Snapshot.StoreNow = now
			attempt := runstate.NodeAttempt{Identity: *subject.Attempt}
			if test.finished {
				finished := now.Add(-test.age)
				attempt.FinishedAt = &finished
			}
			records.view.Attempts = []runstate.NodeAttempt{attempt}
			verdict, found, err := records.reader().NodeLogCompleteness(t.Context(), "run", "job")
			if err != nil || !found || verdict.State != test.state || records.gets != 1 {
				t.Fatalf("verdict=%+v,found=%v,gets=%d,err=%v", verdict, found, records.gets, err)
			}
		})
	}
}

func TestCommittedDashboardGraceUsesSelectedAttemptProperty(t *testing.T) {
	property := func(ordinal, generation uint8, age int16, finished bool) bool {
		records := newScriptedSegmentLogs()
		subject := jobLogSubject(1+int64(ordinal), 1+int64(generation))
		records.add(t, subject, "selected\n", false)
		now := time.Unix(1000, 0)
		records.view.Snapshot.StoreNow = now
		old := now.Add(-24 * time.Hour)
		records.view.Snapshot.Nodes = []runstate.NodeRow{{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Status: runstate.StatusDone, FinishedAt: &old}}
		selected := runstate.NodeAttempt{Identity: *subject.Attempt}
		at := now.Add(-limits.LogSealGrace - time.Duration(age)*time.Nanosecond)
		if finished {
			selected.FinishedAt = &at
		}
		foreign := runstate.NodeAttempt{Identity: *subject.Attempt, FinishedAt: &old}
		for _, dimension := range []string{"team", "run", "job", "attempt", "generation"} {
			other := foreign
			switch dimension {
			case "team":
				other.Identity.Team = "other"
			case "run":
				other.Identity.RunID = "other"
			case "job":
				other.Identity.NodeID = "other"
			case "attempt":
				other.Identity.Attempt++
			case "generation":
				other.Identity.Generation++
			}
			records.view.Attempts = append(records.view.Attempts, other)
		}
		records.view.Attempts = append(records.view.Attempts, selected)
		verdict, found, err := records.reader().NodeLogCompleteness(t.Context(), "run", "job")
		want := logseg.StateStreaming
		if finished && age >= 0 {
			want = logseg.StateIncomplete
		}
		return err == nil && found && verdict.State == want && records.gets == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 144, Rand: rand.New(rand.NewSource(625024))}); err != nil {
		t.Fatal(err)
	}
}
