package storeurl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// TestRoundTrip_FS_KeyConventions catches divergence in path
// conventions between the constructor and read paths.
func TestRoundTrip_FS_KeyConventions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	url := "fs://" + dir
	ctx := context.Background()

	writer, err := OpenArtifactStore(ctx, url)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	if err := writer.Put(ctx, "abcd1234", bytes.NewReader([]byte("payload"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reader, err := OpenArtifactStore(ctx, url)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	rc, err := reader.Get(ctx, "abcd1234")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "payload" {
		t.Fatalf("Get = %q, want payload", got)
	}

	logURL := "fs://" + t.TempDir()
	w2, _ := OpenLogStore(ctx, logURL)
	if err := w2.Append(ctx, "run1", "node1", []byte(`{"msg":"hi"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	r2, _ := OpenLogStore(ctx, logURL)
	got2, err := r2.Read(ctx, "run1", "node1", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(string(got2), `"hi"`) {
		t.Fatalf("Read = %q, want hi", got2)
	}
}

// TestRealBucket_S3 round-trips against a real S3 bucket. Gated on
// $SPARKWING_S3_TEST_BUCKET. Per-run prefix avoids stomping on
// existing cache contents.
func TestRealBucket_S3(t *testing.T) {
	bucket := os.Getenv("SPARKWING_S3_TEST_BUCKET")
	if bucket == "" {
		t.Skip("SPARKWING_S3_TEST_BUCKET not set; skipping real-bucket test")
	}
	ctx := context.Background()

	// Rooted under "test/" so stray runs are easy to scrub.
	keyPrefix := "test/" + randID(t)

	artURL := "s3://" + bucket + "/" + keyPrefix + "/cache"
	logURL := "s3://" + bucket + "/" + keyPrefix + "/logs"

	art, err := OpenArtifactStore(ctx, artURL)
	if err != nil {
		t.Fatalf("OpenArtifactStore: %v", err)
	}
	logs, err := OpenLogStore(ctx, logURL)
	if err != nil {
		t.Fatalf("OpenLogStore: %v", err)
	}

	key := "rt-" + randID(t)
	t.Cleanup(func() { _ = art.Delete(context.Background(), key) })

	if err := art.Put(ctx, key, bytes.NewReader([]byte("hello-real-bucket"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := art.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello-real-bucket" {
		t.Fatalf("Get = %q", got)
	}

	runID := "rt-" + randID(t)
	t.Cleanup(func() { _ = logs.DeleteRun(context.Background(), runID) })

	if err := logs.Append(ctx, runID, "n1", []byte(`{"msg":"hello"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := logs.Append(ctx, runID, "n1", []byte(`{"msg":"world"}`)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	out, err := logs.Read(ctx, runID, "n1", storage.ReadOpts{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(string(out), "hello") || !strings.Contains(string(out), "world") {
		t.Fatalf("Read = %q", out)
	}
}

func randID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
