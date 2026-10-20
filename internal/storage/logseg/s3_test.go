package logseg_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/store"
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
	backend, err := blob.NewS3(client, "bucket", "")
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

func subjectUnit(subject blob.LogSubject) blob.LogUnitKind {
	if subject.Unit != nil {
		return subject.Unit.Kind
	}
	return ""
}
