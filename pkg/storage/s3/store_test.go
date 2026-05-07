package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
)

const testBucket = "sparkwing-test"

// fakeS3 spins up an in-memory S3 server (gofakes3).
func fakeS3(t *testing.T) (*awss3.Client, func()) {
	t.Helper()
	backend := s3mem.New()
	faker := gofakes3.New(backend)
	srv := httptest.NewServer(faker.Server())

	client := awss3.New(awss3.Options{
		Region:             "us-east-1",
		BaseEndpoint:       aws.String(srv.URL),
		UsePathStyle:       true,
		Credentials:        credentials.NewStaticCredentialsProvider("test", "test", ""),
		EndpointResolverV2: awss3.NewDefaultEndpointResolverV2(),
	})

	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(testBucket),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return client, srv.Close
}

func TestArtifactStore_RoundTrip(t *testing.T) {
	t.Parallel()
	client, closer := fakeS3(t)
	defer closer()

	s := NewArtifactStore(testBucket, "cache", client)
	ctx := context.Background()

	if err := s.Put(ctx, "abcd1234", bytes.NewReader([]byte("hello"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, "abcd1234")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Fatalf("Get = %q", got)
	}

	has, err := s.Has(ctx, "abcd1234")
	if err != nil || !has {
		t.Fatalf("Has = (%v, %v)", has, err)
	}
	has, _ = s.Has(ctx, "missing")
	if has {
		t.Fatalf("Has(missing) = true")
	}

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrNotFound", err)
	}

	if err := s.Delete(ctx, "abcd1234"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if has, _ := s.Has(ctx, "abcd1234"); has {
		t.Fatalf("Has after Delete = true")
	}
}

func TestLogStore_RoundTrip(t *testing.T) {
	t.Parallel()
	client, closer := fakeS3(t)
	defer closer()

	ls := NewLogStore(testBucket, "logs", client)
	ctx := context.Background()

	if err := ls.Append(ctx, "run1", "n1", []byte(`{"msg":"hello"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ls.Append(ctx, "run1", "n1", []byte(`{"msg":"world"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ls.Append(ctx, "run1", "n2", []byte(`{"msg":"alpha"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := ls.Read(ctx, "run1", "n1", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(string(got), "hello") || !strings.Contains(string(got), "world") {
		t.Fatalf("Read = %q", got)
	}

	got, err = ls.Read(ctx, "run1", "n1", storage.ReadOpts{Tail: 1})
	if err != nil {
		t.Fatalf("Read tail: %v", err)
	}
	if strings.Contains(string(got), "hello") || !strings.Contains(string(got), "world") {
		t.Fatalf("Read tail = %q", got)
	}

	got, err = ls.ReadRun(ctx, "run1")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if !strings.Contains(string(got), "=== n1 ===") || !strings.Contains(string(got), "=== n2 ===") {
		t.Fatalf("ReadRun = %q", got)
	}

	got, err = ls.Read(ctx, "run1", "missing", storage.ReadOpts{})
	if err != nil || got != nil {
		t.Fatalf("Read missing = (%q, %v)", got, err)
	}

	if err := ls.DeleteRun(ctx, "run1"); err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	got, _ = ls.ReadRun(ctx, "run1")
	if len(got) != 0 {
		t.Fatalf("ReadRun after delete = %q", got)
	}
	if err := ls.DeleteRun(ctx, "run1"); err != nil {
		t.Fatalf("DeleteRun idempotent: %v", err)
	}
}

func TestArtifactStore_PrefixIsolation(t *testing.T) {
	// Two stores with different prefixes in the same bucket must not
	// see each other's keys.
	t.Parallel()
	client, closer := fakeS3(t)
	defer closer()

	a := NewArtifactStore(testBucket, "cache", client)
	b := NewArtifactStore(testBucket, "other", client)
	ctx := context.Background()

	if err := a.Put(ctx, "k", bytes.NewReader([]byte("A"))); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := b.Put(ctx, "k", bytes.NewReader([]byte("B"))); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	rc, _ := a.Get(ctx, "k")
	gotA, _ := io.ReadAll(rc)
	rc.Close()
	rc, _ = b.Get(ctx, "k")
	gotB, _ := io.ReadAll(rc)
	rc.Close()
	if string(gotA) != "A" || string(gotB) != "B" {
		t.Fatalf("isolation broken: a=%q b=%q", gotA, gotB)
	}
}

func TestArtifactStore_List(t *testing.T) {
	t.Parallel()
	client, closer := fakeS3(t)
	defer closer()

	s := NewArtifactStore(testBucket, "cache", client)
	ctx := context.Background()

	put := func(key string) {
		t.Helper()
		if err := s.Put(ctx, key, bytes.NewReader([]byte("x"))); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}
	put("runs/abc/state.ndjson")
	put("runs/def/state.ndjson")
	put("bin/some-key")

	got, err := s.List(ctx, "runs/")
	if err != nil {
		t.Fatalf("List runs/: %v", err)
	}
	want := map[string]bool{
		"runs/abc/state.ndjson": true,
		"runs/def/state.ndjson": true,
	}
	if len(got) != len(want) {
		t.Fatalf("List runs/ = %v, want %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}

	// Sibling-prefix store must not show up under our List.
	other := NewArtifactStore(testBucket, "other", client)
	if err := other.Put(ctx, "runs/xyz/state.ndjson", bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("Put other: %v", err)
	}
	got, _ = s.List(ctx, "runs/")
	for _, k := range got {
		if strings.Contains(k, "xyz") {
			t.Errorf("List leaked sibling-prefix key %q", k)
		}
	}
}
