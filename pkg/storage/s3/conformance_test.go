package s3

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
)

func TestConformance_ArtifactStore(t *testing.T) {
	client, closer := fakeS3(t)
	defer closer()

	var counter uint64
	conformance.TestArtifactStore(t, func() storage.ArtifactStore {
		n := atomic.AddUint64(&counter, 1)
		prefix := fmt.Sprintf("conformance-%d", n)
		return NewArtifactStore(testBucket, prefix, client)
	})
}

func TestConformance_ConditionalWriter(t *testing.T) {
	client, closer := fakeS3(t)
	defer closer()

	var counter uint64
	conformance.TestConditionalWriter(t, func() storage.ArtifactStore {
		n := atomic.AddUint64(&counter, 1)
		prefix := fmt.Sprintf("conformance-cas-%d", n)
		return NewArtifactStore(testBucket, prefix, client)
	})
}

func TestConformance_ConditionalWriterAcrossHandles(t *testing.T) {
	client, closer := fakeS3(t)
	defer closer()

	var counter uint64
	conformance.TestConditionalWriterAcrossHandles(t, func() (storage.ArtifactStore, storage.ArtifactStore) {
		n := atomic.AddUint64(&counter, 1)
		prefix := fmt.Sprintf("conformance-cas-handles-%d", n)
		return NewArtifactStore(testBucket, prefix, client), NewArtifactStore(testBucket, prefix, client)
	})
}

func TestConformance_LogStore(t *testing.T) {
	client, closer := fakeS3(t)
	defer closer()

	var counter uint64
	conformance.TestLogStore(t, func() storage.LogStore {
		n := atomic.AddUint64(&counter, 1)
		prefix := fmt.Sprintf("conformance-logs-%d", n)
		return NewLogStore(testBucket, prefix, client)
	})
}

func TestLogReadRunSeparatesHierarchicalNodes(t *testing.T) {
	client, closer := fakeS3(t)
	defer closer()
	logs := NewLogStore(testBucket, "hierarchical", client)
	for _, node := range []string{"parent", "parent/child", "parent/child/grandchild"} {
		if err := logs.Append(t.Context(), "run-1", node, []byte(node+" line\n")); err != nil {
			t.Fatal(err)
		}
	}
	body, err := logs.ReadRun(t.Context(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	want := "=== parent ===\nparent line\n=== parent/child ===\nparent/child line\n=== parent/child/grandchild ===\nparent/child/grandchild line\n"
	if string(body) != want {
		t.Fatalf("ReadRun = %q, want %q", body, want)
	}
}
