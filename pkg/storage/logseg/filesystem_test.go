package logseg_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type logDataFixture struct {
	signer    logseg.Signer
	reader    logseg.CommittedReader
	view      store.LogSegments
	commitErr error
	verifyErr error
	signs     int
	checks    int
	actor     store.Actor
}

func (f *logDataFixture) VerifyUpload(ctx context.Context, check blob.LogUploadCheck) error {
	f.checks++
	if f.verifyErr != nil {
		return f.verifyErr
	}
	if !reflect.DeepEqual(check.Subject, f.view.Subject) {
		return blob.ErrDigestNotReadable
	}
	return (logseg.UploadVerifier{Objects: f.reader.Objects}).Verify(ctx, check, "r90")
}

func (f *logDataFixture) SignUpload(ctx context.Context, req blob.LogSigningRequest) (blob.LogUploadURL, error) {
	f.signs++
	return f.signer.Sign(ctx, req, "r90")
}

func (f *logDataFixture) ReadLog(ctx context.Context, req blob.LogReadRequest) (io.ReadCloser, error) {
	var out bytes.Buffer
	_, err := f.reader.Read(ctx, f.actor, req, &out)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(&out), nil
}

func (f *logDataFixture) RecordSegments(_ context.Context, commit store.SegmentCommit) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	if !reflect.DeepEqual(commit.Subject, f.view.Subject) || commit.RetentionClass != "r90" || commit.Actor != f.actor {
		return errors.New("unexpected record binding")
	}
	if commit.Final {
		if !slices.Equal(commit.Segments, f.view.Segments) {
			return errors.New("unexpected final segment list")
		}
		f.view.Complete = commit.IncompleteReason == ""
		f.view.IncompleteReason = commit.IncompleteReason
	} else {
		f.view.Segments = append(f.view.Segments, commit.Segments...)
	}
	return nil
}

func (f *logDataFixture) ReadLogSegments(_ context.Context, actor store.Actor, req blob.LogReadRequest) (store.LogSegments, error) {
	if !reflect.DeepEqual(req.Subject, f.view.Subject) || actor != f.actor {
		return store.LogSegments{}, blob.ErrDigestNotReadable
	}
	return f.view, nil
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	t.handler.ServeHTTP(response, req)
	return response.Result(), nil
}

func bindFilesystemLogs(t *testing.T, subject blob.LogSubject) (*logDataFixture, *blob.Filesystem, logseg.Data) {
	t.Helper()
	backend, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.test", SigningKey: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	f := &logDataFixture{view: store.LogSegments{Subject: subject}, actor: store.Actor{Holder: "writer", Generation: 7}}
	f.signer = logseg.Signer{Objects: backend, ClassReady: func(_ context.Context, class string) error {
		if class != "r90" {
			return errors.New("missing lifecycle rule")
		}
		return nil
	}}
	f.reader = logseg.CommittedReader{Records: f, Objects: backend}
	data := logseg.Data{Subject: subject, Transport: f, Records: f, Actor: f.actor, HTTPClient: blob.TransferClient{Transport: handlerTransport{backend}}}
	return f, backend, data
}

func TestFilesystemTaggedLogRoundTrip(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		t.Run(string(subject.Kind)+fmt.Sprint(subject.Unit), func(t *testing.T) {
			_, backend, data := bindFilesystemLogs(t, subject)
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			sink := newSink(t, u, logseg.Options{Subject: subject, SegmentBytes: 8})
			for i := 1; i <= 20; i++ {
				if err := sink.Append([]byte("line\n"), "writer", int64(i), int64(i)); err != nil {
					t.Fatal(err)
				}
			}
			sink.WriterSealed("writer", 20, 0)
			if _, err := sink.Close(t.Context(), logseg.End{}); err != nil {
				t.Fatal(err)
			}
			for _, offset := range []int64{0, 3, 19, 100, 150} {
				stream, err := data.Read(t.Context(), offset, false)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(stream)
				err = errors.Join(err, stream.Close())
				want := strings.Repeat("line\n", 20)
				if string(got) != want[min(int(offset), len(want)):] || err != nil {
					t.Fatalf("read from %d = %q, %v", offset, got, err)
				}
			}
			prefix, err := blob.LogSubjectPrefix("r90", subject)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := backend.Get(t.Context(), blob.LogManifestKey(prefix))
			if err != nil {
				t.Fatal(err)
			}
			var m logseg.Manifest
			err = json.NewDecoder(manifest).Decode(&m)
			if err = errors.Join(err, manifest.Close()); err != nil {
				t.Fatal(err)
			}
			if m.Subject == nil || !reflect.DeepEqual(*m.Subject, subject) || m.Lines != 20 || m.Bytes != 100 || m.Status != logseg.StatusComplete {
				t.Fatalf("manifest = %+v", m)
			}
		})
	}
}

func TestFilesystemTaggedLogRetriesUncertainCommits(t *testing.T) {
	f, _, data := bindFilesystemLogs(t, jobSubject())
	u, err := logseg.NewSubjectUploader(data)
	if err != nil {
		t.Fatal(err)
	}
	seg, body := subjectSegment(t, 1, "line\n")
	f.commitErr = errors.New("uncertain commit")
	if err := u.PutSegment(t.Context(), seg, body); !errors.Is(err, f.commitErr) {
		t.Fatalf("uncertain segment = %v", err)
	}
	f.commitErr = nil
	if err := u.PutSegment(t.Context(), seg, body); err != nil {
		t.Fatalf("segment retry = %v", err)
	}
	m := logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}, SealedAt: time.Now()}
	f.commitErr = errors.New("uncertain final commit")
	if err := u.PutManifest(t.Context(), m); !errors.Is(err, f.commitErr) {
		t.Fatalf("uncertain final = %v", err)
	}
	f.commitErr = nil
	if err := u.PutManifest(t.Context(), m); err != nil {
		t.Fatalf("final retry = %v", err)
	}
	if len(f.view.Segments) != 1 || !f.view.Complete {
		t.Fatalf("committed view = %+v", f.view)
	}
	var out bytes.Buffer
	result, err := f.reader.Read(t.Context(), f.actor, blob.LogReadRequest{Subject: jobSubject()}, &out)
	if err != nil || result.Bytes != 5 || !result.Complete || out.String() != "line\n" {
		t.Fatalf("read = %q, %+v, %v", out.String(), result, err)
	}
	f.actor.Holder = "other"
	if _, err := f.reader.Read(t.Context(), data.Actor, blob.LogReadRequest{Subject: jobSubject()}, io.Discard); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("read authority = %v", err)
	}
}
