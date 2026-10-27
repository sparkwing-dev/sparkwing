package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const sourceS3Host = "objects.invalid"

type objectHostRoute struct{ objects, controller http.RoundTripper }

func (r objectHostRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == sourceS3Host {
		return r.objects.RoundTrip(req)
	}
	return r.controller.RoundTrip(req)
}

// sourceS3 serves the S3 calls the data backend makes and, like S3, refuses a
// request that lacks a header its presigned URL signed.
type sourceS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *sourceS3) Do(r *http.Request) (*http.Response, error) { return s.RoundTrip(r) }

func (s *sourceS3) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: r}
	for _, name := range strings.Split(r.URL.Query().Get("X-Amz-SignedHeaders"), ";") {
		if name != "" && name != "host" && r.Header.Get(name) == "" {
			out.StatusCode = http.StatusForbidden
			out.Body = io.NopCloser(strings.NewReader(`<Error><Code>SignatureDoesNotMatch</Code></Error>`))
			return out, nil
		}
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	etag := func(body []byte) string { return fmt.Sprintf(`"%x"`, sha256.Sum256(body)) }
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		body, ok := s.objects[key]
		if !ok {
			out.StatusCode = http.StatusNotFound
			out.Body = io.NopCloser(strings.NewReader(`<Error><Code>NoSuchKey</Code></Error>`))
			break
		}
		out.ContentLength = int64(len(body))
		out.Header.Set("Content-Length", fmt.Sprint(len(body)))
		out.Header.Set("ETag", etag(body))
		out.Body = io.NopCloser(bytes.NewReader(body))
	case http.MethodPut:
		if copySource := r.Header.Get("X-Amz-Copy-Source"); copySource != "" {
			copySource, _ = url.PathUnescape(copySource)
			body, ok := s.objects[strings.TrimPrefix(copySource, "bucket/")]
			if _, exists := s.objects[key]; !ok || exists || r.Header.Get("X-Amz-Copy-Source-If-Match") != etag(body) {
				out.StatusCode = http.StatusPreconditionFailed
				out.Body = io.NopCloser(strings.NewReader(`<Error><Code>PreconditionFailed</Code></Error>`))
				break
			}
			s.objects[key] = bytes.Clone(body)
			out.Body = io.NopCloser(strings.NewReader(`<CopyObjectResult><ETag>"copied"</ETag></CopyObjectResult>`))
			break
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			out.StatusCode = http.StatusBadRequest
			out.Body = io.NopCloser(strings.NewReader(`<Error><Code>BadDigest</Code></Error>`))
			break
		}
		s.objects[key] = body
	case http.MethodDelete:
		delete(s.objects, key)
		out.StatusCode = http.StatusNoContent
	default:
		return nil, fmt.Errorf("unexpected S3 request %s %s", r.Method, r.URL)
	}
	return out, nil
}

// A deployment on plain S3, with the SDK's default response-checksum mode,
// must hand the source fetcher a bundle URL it can fetch bare.
func TestSourceBundleOverS3ReachesFetcher(t *testing.T) {
	fake := &sourceS3{objects: map[string][]byte{}}
	s3Client := s3.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture-access", "fixture-secret", "fixture-session"),
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenSupported, HTTPClient: fake, RetryMaxAttempts: 1,
	}, func(o *s3.Options) { o.BaseEndpoint, o.UsePathStyle = aws.String("https://"+sourceS3Host), true })
	objects, err := blob.NewS3(s3Client, "bucket", "")
	if err != nil {
		t.Fatal(err)
	}
	s, options, input, _ := sourceUploadFixtureWith(t, objects, fake)
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	repo, sha := sourcetest.Repository(t)
	sourcetest.Git(t, repo, "bundle", "create", path, "HEAD")
	input.Git.SHA, input.Source.Ref = sha, sha
	digest, err := client.UploadSourceSnapshot(t.Context(), options, source.Publication{Git: input.Git, Source: input.Source, Path: path, Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t), Masker: secrets.NewMasker()}, Secrets: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	input.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	c, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := c.CreateRun(t.Context(), controllerwire.CreateRunRequest{IdempotencyKey: "s3", Request: input})
	if err != nil {
		t.Fatal(err)
	}
	engineHTTPRoot(t, newEngineFixture(s.store, EngineController{}), "a", "s3-runner", []capability.Grant{capability.TriggersClaim})
	s.engine.claimProfiles = engineProfileFake{}
	claim, err := s.store.AsOperator().ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "s3-runner", Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: runspec.RunID(created.RunID)}})
	if err != nil || len(claim.Bearer) == 0 {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	awarded := options
	awarded.Bearer = func(context.Context) (string, error) { return string(claim.Bearer), nil }
	spec := source.Spec{RepoKey: string(input.Git.Repository.Key), RemoteURL: input.Source.Location.RemoteURL, SHA: input.Source.Ref, PipelineDir: ".sparkwing", BundleDigest: digest}
	access, err := client.NewSourceAccess(awarded, created.RunID, spec)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := source.PrepareCheckout(t.Context(), spec, source.Options{Workspace: t.TempDir(), Access: access, BundleTransport: options.HTTP.Transport, BuildEnvironment: sourcetest.BuildEnvironment})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	if entry.SHA != sha {
		t.Fatalf("bundle HEAD=%s, want %s", entry.SHA, sha)
	}
}
