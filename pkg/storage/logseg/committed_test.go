package logseg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type committedRecords struct {
	view    store.LogSegments
	err     error
	actor   store.Actor
	request blob.LogReadRequest
	calls   int
}

func (f *committedRecords) ReadLogSegments(_ context.Context, actor store.Actor, request blob.LogReadRequest) (store.LogSegments, error) {
	f.actor, f.request = actor, request
	f.calls++
	return f.view, f.err
}

func committedFixture(t *testing.T, subject blob.LogSubject) (logseg.CommittedReader, *committedRecords, *memObjects, string) {
	t.Helper()
	prefix, err := logseg.SubjectPrefix("r90", subject)
	if err != nil {
		t.Fatal(err)
	}
	f := &committedRecords{view: store.LogSegments{Subject: subject, Complete: true}}
	objects := newMemObjects()
	var offset int64
	var raw strings.Builder
	for i := 1; i <= 3; i++ {
		line := strings.Repeat(string(rune('a'+i-1)), i) + "\n"
		seg, body := subjectSegment(t, i, line)
		key := logseg.SegmentKey(prefix, i)
		objects.put(key, body)
		f.view.Segments = append(f.view.Segments, store.Segment{Index: int64(i), Offset: offset, Size: seg.Bytes, Digest: seg.SHA256, ObjectKey: key})
		offset += seg.Bytes
		raw.WriteString(line)
	}
	return logseg.CommittedReader{Records: f, Objects: objects}, f, objects, raw.String()
}

func TestCommittedReadsVerifyBytesAndResume(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		for _, offset := range []int64{0, 1, 2, 4, 5, 9, 12} {
			r, f, _, want := committedFixture(t, subject)
			request := blob.LogReadRequest{Subject: subject, Offset: offset}
			if offset >= 5 {
				f.view.Segments = f.view.Segments[2:]
			} else if offset >= 2 {
				f.view.Segments = f.view.Segments[1:]
			}
			var out bytes.Buffer
			actor := store.Actor{Holder: "reader"}
			result, err := r.Read(t.Context(), actor, request, &out)
			if err != nil || out.String() != want[min(int(offset), len(want)):] || result.Bytes != int64(out.Len()) || !result.Complete || f.actor != actor || f.request.Offset != offset || f.calls != 1 {
				t.Fatalf("offset %d: result = %+v, %v; bytes = %q", offset, result, err, out.String())
			}
		}
	}
}

func TestCommittedReadsRejectSubstitutedMetadata(t *testing.T) {
	for name, change := range map[string]func(*store.LogSegments){
		"subject":    func(v *store.LogSegments) { v.Subject = ownerSubject(blob.LogPlan) },
		"generation": func(v *store.LogSegments) { a := *v.Subject.Attempt; a.Generation++; v.Subject.Attempt = &a },
		"gap":        func(v *store.LogSegments) { v.Segments[1].Offset++ },
		"overlap":    func(v *store.LogSegments) { v.Segments[1].Offset-- },
		"index":      func(v *store.LogSegments) { v.Segments[1].Index++ },
		"size":       func(v *store.LogSegments) { v.Segments[0].Size = -1 },
		"digest":     func(v *store.LogSegments) { v.Segments[0].Digest = "" },
		"object key": func(v *store.LogSegments) { v.Segments[0].ObjectKey = "teams/acme/cas/unrelated" },
		"foreign object": func(v *store.LogSegments) {
			v.Segments[0].ObjectKey = strings.Replace(v.Segments[0].ObjectKey, "teams/acme/", "teams/other/", 1)
		},
		"retention change": func(v *store.LogSegments) {
			v.Segments[1].ObjectKey = strings.Replace(v.Segments[1].ObjectKey, "logs/r90/", "logs/r30/", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, f, _, _ := committedFixture(t, jobSubject())
			change(&f.view)
			var out bytes.Buffer
			result, err := r.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: jobSubject()}, &out)
			if !errors.Is(err, blob.ErrIntegrity) || result.Bytes != 0 || out.Len() != 0 {
				t.Fatalf("read = %+v, %v; bytes = %q", result, err, out.String())
			}
		})
	}
}

func TestCommittedReadsStopBeforeCorruptBytes(t *testing.T) {
	for _, fault := range []string{"corrupt", "wrong size", "missing"} {
		t.Run(fault, func(t *testing.T) {
			r, f, objects, _ := committedFixture(t, jobSubject())
			second := &f.view.Segments[1]
			switch fault {
			case "corrupt":
				objects.put(second.ObjectKey, []byte("corrupt"))
			case "wrong size":
				second.Size++
				f.view.Segments[2].Offset++
			case "missing":
				objects.mu.Lock()
				delete(objects.objs, second.ObjectKey)
				objects.mu.Unlock()
			}
			var out bytes.Buffer
			result, err := r.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: jobSubject()}, &out)
			if err == nil || result.Complete || result.Bytes != 2 || out.String() != "a\n" {
				t.Fatalf("read = %+v, %v; bytes = %q", result, err, out.String())
			}
		})
	}
}

type shortLogWriter struct{}

func (shortLogWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }

func TestCommittedReadsReportExactlyEmittedBytesOnFailure(t *testing.T) {
	r, _, _, _ := committedFixture(t, jobSubject())
	result, err := r.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: jobSubject()}, shortLogWriter{})
	if !errors.Is(err, io.ErrShortWrite) || result.Bytes != 1 || result.Complete {
		t.Fatalf("read = %+v, %v", result, err)
	}
}

func TestCommittedReadsRequireFreshAuthorization(t *testing.T) {
	r, f, _, _ := committedFixture(t, jobSubject())
	var out bytes.Buffer
	request := blob.LogReadRequest{Subject: jobSubject()}
	if _, err := r.Read(t.Context(), store.Actor{}, request, &out); err != nil {
		t.Fatal(err)
	}
	f.err = blob.ErrDigestNotReadable
	out.Reset()
	result, err := r.Read(t.Context(), store.Actor{}, request, &out)
	if !errors.Is(err, f.err) || f.calls != 2 || out.Len() != 0 || result.Bytes != 0 {
		t.Fatalf("read = %+v, %v; calls = %d", result, err, f.calls)
	}
}

func TestCommittedReadsPreserveIncompleteAndEmptyTails(t *testing.T) {
	r, f, _, _ := committedFixture(t, ownerSubject(blob.LogEval))
	f.view.Complete, f.view.IncompleteReason, f.view.Segments = false, "runner lost", nil
	var out bytes.Buffer
	result, err := r.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: f.view.Subject}, &out)
	if err != nil || result.Complete || result.IncompleteReason != "runner lost" || result.Bytes != 0 {
		t.Fatalf("read = %+v, %v", result, err)
	}
}

type failedLogObjects struct {
	logseg.Objects
	body io.ReadCloser
	err  error
}

func (o failedLogObjects) Get(context.Context, string) (io.ReadCloser, error) { return o.body, o.err }

func TestCommittedReadsFailBeforePublishingUnclosedObjects(t *testing.T) {
	for _, getFails := range []bool{false, true} {
		r, f, objects, _ := committedFixture(t, jobSubject())
		closeErr := errors.New("close failed")
		body := &observedLogBody{Reader: bytes.NewReader(objects.objs[f.view.Segments[0].ObjectKey]), err: closeErr}
		o := failedLogObjects{body: body}
		if getFails {
			o.err = errors.New("get failed")
		}
		r.Objects = o
		var out bytes.Buffer
		result, err := r.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: jobSubject()}, &out)
		if !errors.Is(err, closeErr) || (getFails && !errors.Is(err, o.err)) || !body.closed || out.Len() != 0 || result.Bytes != 0 {
			t.Fatalf("read = %+v, %v; closed = %v", result, err, body.closed)
		}
	}
}

func TestCommittedReadsPreserveCancellation(t *testing.T) {
	r, f, _, _ := committedFixture(t, jobSubject())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	result, err := r.Read(ctx, store.Actor{}, blob.LogReadRequest{Subject: jobSubject()}, &out)
	if !errors.Is(err, context.Canceled) || result.Bytes != 0 || f.calls != 0 {
		t.Fatalf("read = %+v, %v; authorizations = %d", result, err, f.calls)
	}
}
