package storeurl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"testing"
)

func TestRoundTrip_FS_KeyConventions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	url := "fs://" + dir
	ctx := context.Background()

	writer, err := OpenArtifactStore(ctx, url)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	if err := writer.PutIfAbsent(ctx, "abcd1234", bytes.NewReader([]byte("payload"))); err != nil {
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
}

func TestRealBucket_S3(t *testing.T) {
	bucket := os.Getenv("SPARKWING_S3_TEST_BUCKET")
	if bucket == "" {
		t.Skip("SPARKWING_S3_TEST_BUCKET not set; skipping real-bucket test")
	}
	ctx := context.Background()

	keyPrefix := "test/" + randID(t)

	artURL := "s3://" + bucket + "/" + keyPrefix + "/cache"

	art, err := OpenArtifactStore(ctx, artURL)
	if err != nil {
		t.Fatalf("OpenArtifactStore: %v", err)
	}

	key := "rt-" + randID(t)
	t.Cleanup(func() { _ = art.Delete(context.Background(), key) })

	if err := art.PutIfAbsent(ctx, key, bytes.NewReader([]byte("hello-real-bucket"))); err != nil {
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
}

func randID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}
