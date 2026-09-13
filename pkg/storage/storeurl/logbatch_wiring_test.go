package storeurl

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logbatch"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

func TestOpenLogStoreFromSpec_S3Coalesces(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "x")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "y")

	spec := backends.Spec{
		Type:          backends.TypeS3,
		Bucket:        "team",
		Prefix:        "logs",
		BatchInterval: 5 * time.Second,
		BatchBytes:    4096,
		MaxLogObjects: 7,
		MaxLogBytes:   1 << 20,
	}
	ls, err := OpenLogStoreFromSpec(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("OpenLogStoreFromSpec: %v", err)
	}
	batcher, ok := ls.(*logbatch.Store)
	if !ok {
		t.Fatalf("s3 logs surface opened as %T, want a batching store", ls)
	}
	t.Cleanup(func() { _ = batcher.Close() })
	if _, ok := batcher.Delegate().(*s3store.LogStore); !ok {
		t.Fatalf("batcher wraps %T, want the s3 log store", batcher.Delegate())
	}
}

func TestOpenLogStoreFromSpec_FilesystemWritesThrough(t *testing.T) {
	spec := backends.Spec{Type: backends.TypeFilesystem, Path: t.TempDir()}
	ls, err := OpenLogStoreFromSpec(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("OpenLogStoreFromSpec: %v", err)
	}
	if _, ok := ls.(*logbatch.Store); ok {
		t.Fatal("filesystem logs surface was wrapped in a batcher; it appends to an open file already")
	}
}
