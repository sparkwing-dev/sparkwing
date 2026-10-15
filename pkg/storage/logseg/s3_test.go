package logseg_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type s3LogHTTP func(*http.Request) (*http.Response, error)

func (f s3LogHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func (f s3LogHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func s3LogBackend(t *testing.T, requests s3LogHTTP) *blob.S3 {
	t.Helper()
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String("https://objects.invalid"), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test-secret"}, nil
		}),
		HTTPClient: requests,
	})
	backend, err := blob.NewS3(client, "bucket")
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestS3CommittedReads(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		for _, fault := range []string{"", "corrupt", "missing", "short"} {
			t.Run(string(subject.Kind)+"/"+string(subjectUnit(subject))+"/"+fault, func(t *testing.T) {
				reader, records, objects, want := committedFixture(t, subject)
				backend := s3LogBackend(t, func(r *http.Request) (*http.Response, error) {
					key := strings.TrimPrefix(r.URL.Path, "/bucket/")
					raw, ok := objects.objs[key]
					if r.Method != http.MethodGet || !ok || r.URL.Query().Has("list-type") || r.URL.Query().Has("prefix") {
						t.Fatalf("unexpected S3 request: %s %s", r.Method, r.URL.Path)
					}
					status := http.StatusOK
					length := len(raw)
					if key == records.view.Segments[1].ObjectKey {
						switch fault {
						case "corrupt":
							raw = bytes.Repeat([]byte{'x'}, length)
						case "missing":
							status, raw = http.StatusNotFound, []byte(`<Error><Code>NoSuchKey</Code></Error>`)
						case "short":
							raw = raw[:len(raw)-1]
						}
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Length": {strconv.Itoa(length)}}, ContentLength: int64(length), Body: io.NopCloser(bytes.NewReader(raw)), Request: r}, nil
				})
				reader.Objects = backend
				for _, offset := range []int64{0, 1, 2} {
					var out bytes.Buffer
					result, err := reader.Read(t.Context(), store.Actor{Holder: "reader"}, blob.LogReadRequest{Subject: subject, Offset: offset}, &out)
					if fault != "" {
						want = "a\n"
					}
					if out.String() != want[offset:] || result.Bytes != int64(out.Len()) || result.Complete != (fault == "") || (err == nil) != (fault == "") {
						t.Fatalf("offset %d: read = %q, %+v, %v", offset, out.String(), result, err)
					}
				}
			})
		}
	}
}

type conditionalLogs struct {
	t           *testing.T
	objects     map[string][]byte
	loseNextPut bool
}

func (s *conditionalLogs) request(r *http.Request) (*http.Response, error) {
	s.t.Helper()
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	var body []byte
	status := http.StatusOK
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") != "*" || !strings.Contains(r.URL.Query().Get("X-Amz-SignedHeaders"), "if-none-match") {
			s.t.Fatal("log PUT can overwrite an object")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil || int64(len(raw)) != r.ContentLength {
			s.t.Fatalf("log upload size = %d, %d, %v", len(raw), r.ContentLength, err)
		}
		if _, exists := s.objects[key]; exists {
			status = http.StatusPreconditionFailed
		} else {
			s.objects[key] = raw
			if s.loseNextPut {
				s.loseNextPut = false
				return nil, errors.New("upload response lost")
			}
		}
	case http.MethodGet:
		var exists bool
		body, exists = s.objects[key]
		if !exists {
			status, body = http.StatusNotFound, []byte(`<Error><Code>NoSuchKey</Code></Error>`)
		}
	default:
		s.t.Fatalf("unexpected log object request: %s %s", r.Method, r.URL.Path)
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Length": {strconv.Itoa(len(body))}}, ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func bindS3Logs(t *testing.T, subject blob.LogSubject) (*logDataFixture, *conditionalLogs, logseg.Data) {
	t.Helper()
	objects := &conditionalLogs{t: t, objects: make(map[string][]byte)}
	requests := s3LogHTTP(objects.request)
	backend := s3LogBackend(t, requests)
	f := &logDataFixture{view: store.LogSegments{Subject: subject}, actor: store.Actor{Holder: "writer", Generation: 7}}
	f.signer = logseg.BatchSigner{Objects: backend, ClassReady: func(context.Context, string) error { return nil }}
	f.reader = logseg.CommittedReader{Records: f, Objects: backend}
	return f, objects, logseg.Data{Subject: subject, Transport: f, Records: f, Actor: f.actor, HTTPClient: blob.TransferClient{Transport: requests}}
}

func TestS3TaggedLogRoundTrip(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		t.Run(string(subject.Kind)+"/"+string(subjectUnit(subject)), func(t *testing.T) {
			f, objects, data := bindS3Logs(t, subject)
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			seg, body := subjectSegment(t, 1, "line\n")
			if err := u.PutSegment(t.Context(), seg, body); err != nil {
				t.Fatal(err)
			}
			m := logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}, SealedAt: time.Now()}
			if err := u.PutManifest(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			stream, err := data.Read(t.Context(), 2, false)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(stream)
			if err = errors.Join(err, stream.Close()); err != nil || string(raw) != "ne\n" || !f.view.Complete || len(f.view.Segments) != 1 || len(objects.objects) != 2 {
				t.Fatalf("round trip = %q, %v, view = %+v, objects = %d", raw, err, f.view, len(objects.objects))
			}
		})
	}
}

func TestS3LogConflictCannotAuthorizeCommit(t *testing.T) {
	for _, fault := range []string{"segment conflict", "manifest conflict"} {
		t.Run(fault, func(t *testing.T) {
			f, objects, data := bindS3Logs(t, jobSubject())
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := logseg.SubjectPrefix("r90", data.Subject)
			if err != nil {
				t.Fatal(err)
			}
			key := logseg.SegmentKey(prefix, 1)
			seg, body := subjectSegment(t, 1, "line\n")
			switch fault {
			case "segment conflict":
				objects.objects[key] = []byte("another payload")
			}
			put := func() error { return u.PutSegment(t.Context(), seg, body) }
			if fault == "manifest conflict" {
				if err := put(); err != nil {
					t.Fatal(err)
				}
				key = logseg.ManifestKey(prefix)
				objects.objects[key] = []byte("another manifest")
				put = func() error {
					return u.PutManifest(t.Context(), logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}})
				}
			}
			if err := put(); err == nil {
				t.Fatal("failed upload or record commit succeeded")
			}
			original := bytes.Clone(objects.objects[key])
			f.commitErr = nil
			if err := put(); err == nil {
				t.Fatal("HTTP 412 was accepted as a matching payload")
			}
			wantSegments := 0
			if fault == "manifest conflict" {
				wantSegments = 1
			}
			if !bytes.Equal(objects.objects[key], original) || f.view.Complete || len(f.view.Segments) != wantSegments {
				t.Fatalf("conflict changed bytes or records: %+v", f.view)
			}
		})
	}
}

func TestS3LogRecoversVerifiedReplay(t *testing.T) {
	for _, final := range []bool{false, true} {
		for _, fault := range []string{"lost PUT response", "failed record commit"} {
			t.Run(strconv.FormatBool(final)+"/"+fault, func(t *testing.T) {
				f, objects, data := bindS3Logs(t, ownerSubject(blob.LogEval))
				u, err := logseg.NewSubjectUploader(data)
				if err != nil {
					t.Fatal(err)
				}
				seg, body := subjectSegment(t, 1, "line\n")
				put := func() error { return u.PutSegment(t.Context(), seg, body) }
				if final {
					if err := put(); err != nil {
						t.Fatal(err)
					}
					m := logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}}
					put = func() error { return u.PutManifest(t.Context(), m) }
				}
				if fault == "lost PUT response" {
					objects.loseNextPut = true
				} else {
					f.commitErr = errors.New("record rejected")
				}
				err = put()
				if fault == "failed record commit" && !errors.Is(err, f.commitErr) {
					t.Fatalf("first commit = %v", err)
				}
				if fault == "lost PUT response" && err != nil {
					t.Fatalf("verified lost response = %v", err)
				}
				f.commitErr = nil
				if err := put(); err != nil {
					t.Fatalf("verified retry = %v", err)
				}
				if len(f.view.Segments) != 1 || f.view.Complete != final || f.checks == 0 {
					t.Fatalf("retry state = %+v, checks = %d", f.view, f.checks)
				}
			})
		}
	}
}

func TestS3LogReplayRequiresCurrentAuthority(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(strconv.FormatBool(final), func(t *testing.T) {
			f, objects, data := bindS3Logs(t, jobSubject())
			u, err := logseg.NewSubjectUploader(data)
			if err != nil {
				t.Fatal(err)
			}
			seg, body := subjectSegment(t, 1, "line\n")
			put := func() error { return u.PutSegment(t.Context(), seg, body) }
			if final {
				if err := put(); err != nil {
					t.Fatal(err)
				}
				m := logseg.Manifest{Status: logseg.StatusComplete, Segments: []logseg.Segment{seg}}
				put = func() error { return u.PutManifest(t.Context(), m) }
			}
			objects.loseNextPut = true
			f.verifyErr = blob.ErrDigestNotReadable
			if err := put(); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("refused verification = %v", err)
			}
			wantSegments := 0
			if final {
				wantSegments = 1
			}
			if f.view.Complete || len(f.view.Segments) != wantSegments {
				t.Fatalf("refused verification created records: %+v", f.view)
			}
			f.verifyErr = nil
			if err := put(); err != nil || f.view.Complete != final || len(f.view.Segments) != 1 {
				t.Fatalf("authorized recovery = %+v, %v", f.view, err)
			}
		})
	}
}

func subjectUnit(subject blob.LogSubject) blob.LogUnitKind {
	if subject.Unit != nil {
		return subject.Unit.Kind
	}
	return ""
}
