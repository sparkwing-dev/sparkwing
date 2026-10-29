package controller

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func committedDashboardLogs(t *testing.T, payload []byte) (segmentLogs, blob.LogSubject, *store.Store) {
	t.Helper()
	return committedDashboardLogSegments(t, payload)
}

func committedDashboardLogSegments(t *testing.T, payloads ...[]byte) (segmentLogs, blob.LogSubject, *store.Store) {
	t.Helper()
	now := time.Now().UTC()
	engine, tenant := engineRuntimeStore(t, teststore.WithClock(func() time.Time { return now }))
	authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
	actor := engineActor(authorized)
	engineAdmitExecution(t, engine.Store, authorized.Principal)
	started, err := tenant.TransitionNode(t.Context(), store.StartNode{Ref: runspec.NodeRef{RunID: "run", NodeID: "job"}, Actor: actor, SourceAttempt: new(runspec.AttemptOrdinal(0))})
	if err != nil {
		t.Fatal(err)
	}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "a", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "a", RunID: "run", NodeID: "job", Attempt: int64(started.Attempt), Generation: int64(started.Generation)}}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{41}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	var segments []runstate.Segment
	var offset int64
	for i, payload := range payloads {
		var compressed bytes.Buffer
		zipper := gzip.NewWriter(&compressed)
		if _, err := zipper.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := zipper.Close(); err != nil {
			t.Fatal(err)
		}
		key, err := blob.LogObjectKey(subject, "r30", int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		signed, _, err := objects.SignLogPut(t.Context(), key, int64(compressed.Len()), blob.Digest(fmt.Sprintf("%x", sha256.Sum256(compressed.Bytes()))), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		objects.ServeHTTP(response, httptest.NewRequest(http.MethodPut, signed, bytes.NewReader(compressed.Bytes())))
		if response.Code != http.StatusCreated {
			t.Fatalf("signed log PUT: %d %s", response.Code, response.Body)
		}
		segments = append(segments, runstate.Segment{Index: int64(i + 1), Offset: offset, Size: int64(len(payload)), StoredBytes: int64(compressed.Len()), Digest: fmt.Sprintf("%x", sha256.Sum256(compressed.Bytes())), ObjectKey: key})
		offset += int64(len(payload))
	}
	if err := tenant.RecordSegments(t.Context(), store.SegmentCommit{Actor: actor, Subject: subject, Segments: segments, Final: true, RetentionClass: "r30"}); err != nil {
		t.Fatal(err)
	}
	return segmentLogs{records: tenant, actor: actor, live: engine.Server.liveLogs, objs: objects}, subject, engine.Store
}

func TestCommittedDashboardLogsReadRealRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.1s measured in the short class; the full suite runs it")
	}
	property := func(seed [16]byte, ordinal uint8) bool {
		payload := []byte(fmt.Sprintf("%x\nsecond\n", seed))
		logs, subject, st := committedDashboardLogs(t, payload)
		body, err := logs.Read(t.Context(), "run", "job", storage.ReadOpts{})
		if err != nil || !bytes.Equal(body, payload) {
			t.Logf("read = %q, %v", body, err)
			return false
		}
		body, err = logs.Read(t.Context(), "run", "job", storage.ReadOpts{Tail: 1})
		if err != nil || string(body) != "second\n" {
			return false
		}
		offset := int64(ordinal) % int64(len(payload))
		var out bytes.Buffer
		result, err := logs.read(t.Context(), subject, offset, &out)
		if err != nil || !result.Complete || result.Bytes != int64(len(payload))-offset || !bytes.Equal(out.Bytes(), payload[offset:]) {
			return false
		}
		engineSQL(t, st, `UPDATE runs SET status='success',finished_at=1 WHERE team='a' AND id='run'`)
		out.Reset()
		if err := logs.streamNode(t.Context(), "run", "job", logCursor{}, &out); err != nil || !strings.Contains(out.String(), "event: stream_end") || strings.Count(out.String(), "data: second") != 1 {
			t.Logf("stream=%s, %v", out.String(), err)
			return false
		}
		out.Reset()
		cursor := logCursor{attempt: 0, off: int64(bytes.IndexByte(payload, '\n') + 1)}
		if err := logs.streamNode(t.Context(), "run", "job", cursor, &out); err != nil || strings.Contains(out.String(), fmt.Sprintf("%x", seed)) || strings.Count(out.String(), "data: second") != 1 {
			t.Logf("resume=%s, %v", out.String(), err)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12, Rand: rand.New(rand.NewSource(71))}); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedDashboardLogsNeverEmitForForeignOrRevokedActors(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 2.1s measured in the short class; the full suite runs it")
	}
	property := func(seed [16]byte) bool {
		logs, subject, st := committedDashboardLogs(t, []byte(fmt.Sprintf("%x\n", seed)))
		foreign := logs.actor
		foreign.CapabilityPrefix = "foreign-" + fmt.Sprintf("%x", seed)
		for _, actor := range []runstate.Actor{foreign, {}} {
			var out bytes.Buffer
			reader := logseg.CommittedReader{Records: logs.records, Objects: logs.objs}
			result, err := reader.Read(t.Context(), actor, blob.LogReadRequest{Subject: subject}, &out)
			if err == nil || out.Len() != 0 || result.Bytes != 0 {
				t.Logf("foreign read = %+v %s, %v", result, out.String(), err)
				return false
			}
		}
		engineSQL(t, st, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, logs.actor.CapabilityPrefix)
		var out bytes.Buffer
		result, err := logs.read(t.Context(), subject, 0, &out)
		if err == nil || out.Len() != 0 || result.Bytes != 0 {
			return false
		}
		body, err := logs.Read(t.Context(), "run", "job", storage.ReadOpts{})
		return err != nil && len(body) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12, Rand: rand.New(rand.NewSource(72))}); err != nil {
		t.Fatal(err)
	}
}

func TestCommittedDashboardLogStreamCloseCancelsPendingReads(t *testing.T) {
	logs, _, _ := committedDashboardLogs(t, []byte("line\n"))
	stream, err := logs.Stream(t.Context(), "run", "job")
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(stream); err == nil {
		t.Fatal("closed log stream remained readable")
	}
}

type countedLogObjects struct {
	logseg.SegmentObjects
	opens int
}

func (o *countedLogObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	o.opens++
	return o.SegmentObjects.Get(ctx, key)
}

type revokeLogWriter struct {
	bytes.Buffer
	afterWrite func()
}

func (w *revokeLogWriter) Write(body []byte) (int, error) {
	n, err := w.Buffer.Write(body)
	w.afterWrite()
	return n, err
}

func TestCommittedDashboardLogsRecheckAuthorityBetweenSegments(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.4s measured in the short class; the full suite runs it")
	}
	property := func(seed [16]byte) bool {
		first := []byte(fmt.Sprintf("%x first\n", seed))
		second := []byte(fmt.Sprintf("%x second\n", seed))
		logs, subject, st := committedDashboardLogSegments(t, first, second)
		objects := &countedLogObjects{SegmentObjects: logs.objs}
		logs.objs = objects
		var out revokeLogWriter
		out.afterWrite = func() {
			engineSQL(t, st, `UPDATE capabilities SET revoked_at=1 WHERE prefix=?`, logs.actor.CapabilityPrefix)
		}
		result, err := logs.read(t.Context(), subject, 0, &out)
		if err == nil || result.Complete || objects.opens != 1 || result.Bytes != int64(len(first)) || !bytes.Equal(out.Bytes(), first) {
			t.Logf("revoked read = %+v, %v, %q", result, err, out.Bytes())
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8, Rand: rand.New(rand.NewSource(73))}); err != nil {
		t.Fatal(err)
	}
}
