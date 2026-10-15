package logseg_test

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
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type subjectFixture struct {
	subject                          blob.LogSubject
	signs                            []blob.LogSigningRequest
	commits                          []store.SegmentCommit
	bodies                           map[string][]byte
	changeBatch                      func(*blob.LogUploadBatch)
	commitErr, transferErr, closeErr error
	read                             blob.LogReadRequest
}

func TestSubjectUploaderSendsSignedHeaders(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	f.changeBatch = func(batch *blob.LogUploadBatch) {
		batch.ManifestHeaders = map[string]string{"If-None-Match": "*", "X-Object": "manifest"}
		for i := range batch.Segments {
			batch.Segments[i].Headers = map[string]string{"If-None-Match": "*", "X-Object": fmt.Sprint(batch.Segments[i].Index)}
		}
	}
	var paths []string
	data.HTTPClient = blob.TransferClient{Transport: logHTTPTransport(func(r *http.Request) (*http.Response, error) {
		want := "1"
		if r.URL.Path == "/manifest" {
			want = "manifest"
		}
		if r.Method != http.MethodPut || r.Header.Get("If-None-Match") != "*" || r.Header.Get("X-Object") != want {
			t.Fatalf("PUT lost signed headers: %s %s, %v", r.Method, r.URL.Path, r.Header)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.ContentLength != int64(len(raw)) {
			t.Fatalf("PUT body = %d, content length = %d, %v", len(raw), r.ContentLength, err)
		}
		paths = append(paths, r.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	if err := u.PutSegment(t.Context(), seg, body); err != nil {
		t.Fatal(err)
	}
	if err := u.PutManifest(t.Context(), logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/segment-1", "/manifest"}) || len(f.commits) != 2 || !f.commits[1].Final {
		t.Fatalf("uploads = %v, commits = %+v", paths, f.commits)
	}
}

type logHTTPTransport func(*http.Request) (*http.Response, error)

func (f logHTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jobSubject() blob.LogSubject {
	return blob.LogSubject{Kind: blob.LogJobAttempt, Team: "acme", RunID: "run-1", Attempt: &runspec.AttemptIdentity{Team: "acme", RunID: "run-1", NodeID: "build/linux", Attempt: 2, Generation: 7}}
}

func ownerSubject(kind blob.LogUnitKind) blob.LogSubject {
	s := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "acme", RunID: "run-1", Unit: &blob.LogUnit{Kind: kind, Generation: 3}}
	if kind == blob.LogEval {
		s.Unit.Source = &blob.LogSource{NodeID: "generator", Generation: 5}
	}
	return s
}

func newSubjectFixture(subject blob.LogSubject) (*subjectFixture, logseg.Data) {
	f := &subjectFixture{subject: subject, bodies: map[string][]byte{}}
	return f, logseg.Data{Subject: subject, Transport: f, HTTPClient: f, Records: f, Actor: store.Actor{Holder: "runner", Generation: 7}}
}

func (f *subjectFixture) SignSegments(_ context.Context, req blob.LogSigningRequest) (blob.LogUploadBatch, error) {
	f.signs = append(f.signs, req)
	b := blob.LogUploadBatch{Subject: req.Subject, ManifestURL: "https://objects.test/manifest", RetentionClass: "r90", ExpiresAt: time.Now().Add(limits.LogUploadURLTTL)}
	for i := 0; i < req.Count; i++ {
		index := req.FirstSegment + int64(i)
		b.Segments = append(b.Segments, blob.LogUploadURL{Index: index, URL: fmt.Sprintf("https://objects.test/segment-%d", index), ExpiresAt: b.ExpiresAt})
	}
	if f.changeBatch != nil {
		f.changeBatch(&b)
	}
	return b, nil
}

func (f *subjectFixture) RecordSegments(_ context.Context, c store.SegmentCommit) error {
	f.commits = append(f.commits, c)
	return f.commitErr
}

type closeError struct {
	io.Reader
	err error
}

func (r closeError) Close() error { return r.err }

func (f *subjectFixture) Transfer(_ context.Context, transfer blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	if f.transferErr != nil {
		return nil, f.transferErr
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if transfer.Size != int64(len(b)) {
		return nil, fmt.Errorf("wrong signed size")
	}
	f.bodies[transfer.URL] = b
	return closeError{Reader: bytes.NewReader(nil), err: f.closeErr}, nil
}

func (f *subjectFixture) ReadLog(_ context.Context, req blob.LogReadRequest) (io.ReadCloser, error) {
	f.read = req
	return io.NopCloser(bytes.NewBufferString("controller-proxied")), nil
}

func (f *subjectFixture) VerifyUpload(context.Context, blob.LogUploadCheck) error {
	return errors.New("upload verification unavailable")
}

func subjectSegment(t *testing.T, n int, raw string) (logseg.Segment, []byte) {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := io.WriteString(w, raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return logseg.Segment{N: n, FirstSeq: int64(n), LastSeq: int64(n), Lines: 1, Bytes: int64(len(raw)), StoredBytes: int64(b.Len()), SHA256: hex.EncodeToString(sum[:])}, b.Bytes()
}

func TestSubjectUploaderCommitsTaggedSegments(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		t.Run(string(subject.Kind)+fmt.Sprint(subject.Unit), func(t *testing.T) {
			f, data := newSubjectFixture(subject)
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			var segments []logseg.Segment
			var offset int64
			for i := 1; i <= limits.LogUploadBatch+1; i++ {
				seg, body := subjectSegment(t, i, "line\n")
				if err := u.PutSegment(t.Context(), seg, body); err != nil {
					t.Fatal(err)
				}
				c := f.commits[len(f.commits)-1]
				if !reflect.DeepEqual(c.Subject, subject) || c.Actor != data.Actor || c.Final || len(c.Segments) != 1 || c.Segments[0].Offset != offset || c.Segments[0].Size != seg.Bytes || c.Segments[0].Digest != seg.SHA256 {
					t.Fatalf("commit = %+v", c)
				}
				offset += seg.Bytes
				segments = append(segments, seg)
			}
			m := logseg.Manifest{Status: logseg.StatusComplete, Segments: segments}
			if err := u.PutManifest(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			if len(f.signs) != 2 || f.signs[0].FirstSegment != 1 || f.signs[1].FirstSegment != 17 || f.signs[0].Count != 16 {
				t.Fatalf("signing = %+v", f.signs)
			}
			last := f.commits[len(f.commits)-1]
			if !last.Final || last.IncompleteReason != "" || len(last.Segments) != 17 || last.RetentionClass != "r90" {
				t.Fatalf("final commit = %+v", last)
			}
			var manifest struct {
				Subject blob.LogSubject `json:"subject"`
			}
			if err := json.Unmarshal(f.bodies["https://objects.test/manifest"], &manifest); err != nil || !reflect.DeepEqual(manifest.Subject, subject) {
				t.Fatalf("manifest = %+v, %v", manifest, err)
			}
		})
	}
}

func TestSubjectUploaderRejectsSubstitutedBatch(t *testing.T) {
	for name, change := range map[string]func(*blob.LogUploadBatch){
		"subject":       func(b *blob.LogUploadBatch) { b.Subject = ownerSubject(blob.LogPlan) },
		"count":         func(b *blob.LogUploadBatch) { b.Segments = b.Segments[:1] },
		"index":         func(b *blob.LogUploadBatch) { b.Segments[1].Index++ },
		"manifest":      func(b *blob.LogUploadBatch) { b.ManifestURL = "" },
		"expired":       func(b *blob.LogUploadBatch) { b.ExpiresAt = time.Time{} },
		"expired_slot":  func(b *blob.LogUploadBatch) { b.Segments[0].ExpiresAt = time.Time{} },
		"duplicate_url": func(b *blob.LogUploadBatch) { b.Segments[1].URL = b.Segments[0].URL },
	} {
		t.Run(name, func(t *testing.T) {
			f, data := newSubjectFixture(jobSubject())
			f.changeBatch = change
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			seg, body := subjectSegment(t, 1, "line\n")
			if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, blob.ErrIntegrity) || len(f.commits) != 0 || len(f.bodies) != 0 {
				t.Fatalf("upload = %v, commits = %d, objects = %d", err, len(f.commits), len(f.bodies))
			}
		})
	}
}

func TestSubjectUploaderFailsLoudly(t *testing.T) {
	for _, fault := range []string{"put", "close", "commit"} {
		t.Run(fault, func(t *testing.T) {
			f, data := newSubjectFixture(jobSubject())
			failure := errors.New(fault)
			switch fault {
			case "put":
				f.transferErr = failure
			case "close":
				f.closeErr = failure
			case "commit":
				f.commitErr = failure
			}
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			seg, body := subjectSegment(t, 1, "line\n")
			if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, failure) {
				t.Fatalf("upload = %v", err)
			}
			if fault != "commit" && len(f.commits) != 0 {
				t.Fatal("failed bytes committed")
			}
			f.transferErr, f.closeErr, f.commitErr = nil, nil, nil
			if err := u.PutSegment(t.Context(), seg, body); err != nil {
				t.Fatalf("retry = %v", err)
			}
			if err := u.PutManifest(t.Context(), logseg.Manifest{Status: logseg.StatusIncomplete, Reason: "lost tail", Segments: []logseg.Segment{seg}}); err != nil {
				t.Fatal(err)
			}
			if got := f.commits[len(f.commits)-1].IncompleteReason; got != "lost tail" {
				t.Fatalf("incomplete reason = %q", got)
			}
		})
	}
}

func TestSubjectReadsUseController(t *testing.T) {
	f, data := newSubjectFixture(ownerSubject(blob.LogEval))
	body, err := data.Read(t.Context(), 9, true)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "controller-proxied" || f.read.Offset != 9 || !f.read.Follow || !reflect.DeepEqual(f.read.Subject, data.Subject) || len(f.signs) != 0 || len(f.bodies) != 0 {
		t.Fatalf("read = %q, %v, request = %+v", got, err, f.read)
	}
}

func TestSubjectUploaderRejectsInvalidSegmentBeforeSigning(t *testing.T) {
	for name, change := range map[string]func(*logseg.Segment, *[]byte){
		"digest":         func(s *logseg.Segment, _ *[]byte) { s.SHA256 = strings.Repeat("0", 64) },
		"missing digest": func(s *logseg.Segment, _ *[]byte) { s.SHA256 = "" },
		"stored size":    func(s *logseg.Segment, _ *[]byte) { s.StoredBytes++ },
		"raw size":       func(s *logseg.Segment, _ *[]byte) { s.Bytes++ },
		"lines":          func(s *logseg.Segment, _ *[]byte) { s.Lines++ },
		"sequence":       func(s *logseg.Segment, _ *[]byte) { s.FirstSeq++; s.LastSeq++ },
		"index":          func(s *logseg.Segment, _ *[]byte) { s.N++ },
		"corruption":     func(_ *logseg.Segment, b *[]byte) { (*b)[0]++ },
	} {
		t.Run(name, func(t *testing.T) {
			f, data := newSubjectFixture(jobSubject())
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			seg, body := subjectSegment(t, 1, "line\n")
			change(&seg, &body)
			if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, blob.ErrIntegrity) || len(f.signs) != 0 || len(f.commits) != 0 || len(f.bodies) != 0 {
				t.Fatalf("upload = %v; signs = %d, commits = %d, objects = %d", err, len(f.signs), len(f.commits), len(f.bodies))
			}
		})
	}
}

func TestSubjectUploaderRetriesAnExactFinalCommit(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	if err := u.PutSegment(t.Context(), seg, body); err != nil {
		t.Fatal(err)
	}
	m := logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}}
	f.commitErr = errors.New("uncertain final commit")
	if err := u.PutManifest(t.Context(), m); !errors.Is(err, f.commitErr) {
		t.Fatalf("final = %v", err)
	}
	raw := bytes.Clone(f.bodies["https://objects.test/manifest"])
	changed := m
	changed.Status, changed.Reason = logseg.StatusIncomplete, "changed tail"
	if err := u.PutManifest(t.Context(), changed); !errors.Is(err, logseg.ErrSealed) {
		t.Fatalf("changed final = %v", err)
	}
	if !bytes.Equal(raw, f.bodies["https://objects.test/manifest"]) {
		t.Fatal("changed final overwrote manifest")
	}
	f.commitErr = nil
	if err := u.PutManifest(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	n := len(f.commits)
	if err := u.PutManifest(t.Context(), m); err != nil || len(f.commits) != n {
		t.Fatalf("final replay = %v, commits = %d", err, len(f.commits))
	}
	seg2, body2 := subjectSegment(t, 2, "late\n")
	if err := u.PutSegment(t.Context(), seg2, body2); !errors.Is(err, logseg.ErrSealed) {
		t.Fatalf("late append = %v", err)
	}
}

func TestSubjectUploaderRefusesChangedRetryBytes(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	f.commitErr = errors.New("uncertain segment commit")
	if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, f.commitErr) {
		t.Fatal(err)
	}
	changed, other := subjectSegment(t, 1, "other\n")
	if err := u.PutSegment(t.Context(), changed, other); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("changed retry = %v", err)
	}
	f.commitErr = nil
	if err := u.PutSegment(t.Context(), seg, body); err != nil {
		t.Fatal(err)
	}
	if err := u.PutSegment(t.Context(), seg, body); err != nil {
		t.Fatal(err)
	}
	if len(f.commits) != 2 {
		t.Fatalf("exact retry committed twice: %d", len(f.commits))
	}
}

func TestSubjectUploaderPinsIdentityAndRetention(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	data.Subject.Attempt.Generation++
	for i := 1; i <= limits.LogUploadBatch; i++ {
		seg, body := subjectSegment(t, i, "line\n")
		if err := u.PutSegment(t.Context(), seg, body); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(f.signs[0].Subject, jobSubject()) {
		t.Fatal("caller changed the upload identity")
	}
	f.changeBatch = func(b *blob.LogUploadBatch) { b.RetentionClass = "r30" }
	seg, body := subjectSegment(t, limits.LogUploadBatch+1, "line\n")
	if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, blob.ErrIntegrity) || len(f.commits) != limits.LogUploadBatch {
		t.Fatalf("retention change = %v", err)
	}
}

func TestSubjectUploaderRejectsFalseCompleteness(t *testing.T) {
	for _, m := range []logseg.Manifest{
		{Status: logseg.StatusComplete, DroppedLines: 1},
		{Status: logseg.StatusComplete, Gaps: []logseg.Gap{{From: 1, To: 2}}},
		{Status: logseg.StatusIncomplete},
		{Status: logseg.StatusIncomplete, Reason: "lost", DroppedLines: -1},
	} {
		f, data := newSubjectFixture(jobSubject())
		u, err := logseg.NewSubjectUploader(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := u.PutManifest(t.Context(), m); !errors.Is(err, blob.ErrIntegrity) || len(f.signs) != 0 || len(f.commits) != 0 {
			t.Fatalf("false completeness = %v, signs = %d", err, len(f.signs))
		}
	}
}

func TestSubjectOperationsPreserveCancellation(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	seg, body := subjectSegment(t, 1, "line\n")
	if err := u.PutSegment(ctx, seg, body); !errors.Is(err, context.Canceled) || len(f.signs) != 0 {
		t.Fatalf("upload = %v, signs = %d", err, len(f.signs))
	}
	if err := u.PutManifest(ctx, logseg.Manifest{Status: logseg.StatusComplete}); !errors.Is(err, context.Canceled) || len(f.signs) != 0 {
		t.Fatalf("final = %v, signs = %d", err, len(f.signs))
	}
	if _, err := data.Read(ctx, 0, false); !errors.Is(err, context.Canceled) || f.read.Subject.Kind != "" {
		t.Fatalf("read = %v", err)
	}
}

type cancellingLogTransfer struct {
	client blob.HTTPClient
	cancel context.CancelFunc
}

func (c cancellingLogTransfer) Transfer(ctx context.Context, transfer blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	response, err := c.client.Transfer(ctx, transfer, body)
	c.cancel()
	return response, errors.Join(err, errors.New("uncertain transfer"))
}

type replayProbe struct {
	blob.LogTransport
	checks int
}

func (p *replayProbe) VerifyUpload(context.Context, blob.LogUploadCheck) error {
	p.checks++
	return nil
}

func TestSubjectCancelledTransferCannotRecoverOrCommit(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probe := &replayProbe{LogTransport: data.Transport}
	data.Transport = probe
	data.HTTPClient = cancellingLogTransfer{client: data.HTTPClient, cancel: cancel}
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	if err := u.PutSegment(ctx, seg, body); !errors.Is(err, context.Canceled) || probe.checks != 0 || len(f.commits) != 0 {
		t.Fatalf("cancelled upload = %v, checks = %d, commits = %d", err, probe.checks, len(f.commits))
	}
}

func TestSubjectPrefixKeepsEveryFenceDistinct(t *testing.T) {
	seen := map[string]bool{}
	subjects := []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)}
	for _, base := range subjects {
		for i := range 4 {
			s := base
			if base.Attempt != nil {
				a := *base.Attempt
				s.Attempt = &a
				s.Attempt.Generation += int64(i)
			} else {
				u := *base.Unit
				s.Unit = &u
				s.Unit.Generation += int64(i)
			}
			key, err := blob.LogSubjectPrefix("r90", s)
			if err != nil || seen[key] {
				t.Fatalf("colliding prefix = %q, %v", key, err)
			}
			seen[key] = true
		}
	}
	eval := ownerSubject(blob.LogEval)
	eval.Unit.Source.Generation++
	prefix, err := blob.LogSubjectPrefix("r90", eval)
	if err != nil || seen[prefix] {
		t.Fatalf("source fence collision = %q, %v", prefix, err)
	}
}

type logReadReply struct {
	blob.LogTransport
	body io.ReadCloser
	err  error
}

func (r logReadReply) ReadLog(context.Context, blob.LogReadRequest) (io.ReadCloser, error) {
	return r.body, r.err
}

type observedLogBody struct {
	io.Reader
	closed bool
	err    error
}

func (b *observedLogBody) Close() error { b.closed = true; return b.err }

func TestSubjectReadsCloseFailedResponses(t *testing.T) {
	_, data := newSubjectFixture(jobSubject())
	requestErr, closeErr := errors.New("read refused"), errors.New("close failed")
	body := &observedLogBody{Reader: strings.NewReader("bytes"), err: closeErr}
	data.Transport = logReadReply{body: body, err: requestErr}
	got, err := data.Read(t.Context(), 0, false)
	if got != nil || !body.closed || !errors.Is(err, requestErr) || !errors.Is(err, closeErr) {
		t.Fatalf("read = %v, %v; closed = %v", got, err, body.closed)
	}
	data.Transport = logReadReply{}
	if _, err := data.Read(t.Context(), 0, false); err == nil {
		t.Fatal("nil response succeeded")
	}
}

func TestSubjectSinkPublishesAttributedManifest(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		f, data := newSubjectFixture(subject)
		u, err := logseg.NewSubjectUploader(data)
		if err != nil {
			t.Fatal(err)
		}
		sink := newSink(t, u, logseg.Options{Subject: subject, SegmentBytes: 8})
		for i := 1; i <= 4; i++ {
			if err := sink.Append([]byte("line\n"), "writer", int64(i), int64(i)); err != nil {
				t.Fatal(err)
			}
		}
		sink.WriterSealed("writer", 4, 0)
		if _, err := sink.Close(t.Context(), logseg.End{}); err != nil {
			t.Fatal(err)
		}
		var manifest logseg.Manifest
		if err := json.Unmarshal(f.bodies["https://objects.test/manifest"], &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.Subject == nil || !reflect.DeepEqual(*manifest.Subject, subject) || manifest.Lines != 4 || manifest.Bytes != 20 || manifest.FirstSeq != 1 || manifest.LastSeq != 4 {
			t.Fatalf("manifest = %+v", manifest)
		}
	}
}

func TestSubjectUploaderSerializesExactReplays(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range cap(errors) {
		wg.Go(func() { errors <- u.PutSegment(t.Context(), seg, body) })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(f.commits) != 1 || len(f.signs) != 1 {
		t.Fatalf("commits = %d, signs = %d", len(f.commits), len(f.signs))
	}
}

func TestSubjectUploaderAcceptsControllerClockAhead(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	f.changeBatch = func(b *blob.LogUploadBatch) {
		b.ExpiresAt = b.ExpiresAt.Add(time.Second)
		for i := range b.Segments {
			b.Segments[i].ExpiresAt = b.ExpiresAt
		}
	}
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	if err := u.PutSegment(t.Context(), seg, body); err != nil {
		t.Fatal(err)
	}
}

func TestSubjectUploaderRefusesCompleteWithUncommittedBytes(t *testing.T) {
	f, data := newSubjectFixture(jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	f.commitErr = errors.New("uncertain commit")
	if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, f.commitErr) {
		t.Fatal(err)
	}
	f.commitErr = nil
	if err := u.PutManifest(t.Context(), logseg.Manifest{Status: logseg.StatusComplete}); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("complete = %v", err)
	}
}
