package s3

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/conformance"
)

// TestConformance_ArtifactStore wires the shared conformance suite
// against an in-memory gofakes3 server. Each subtest gets a fresh
// store via a unique prefix under the same bucket.
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

// TestConformance_ConditionalWriter runs the optional CAS-capability
// suite against the in-memory gofakes3 server, which enforces
// If-None-Match / If-Match preconditions.
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

// TestConformance_LogStore wires the shared conformance suite for
// log stores against the in-memory gofakes3 server. Same isolation
// approach: unique prefix per factory call.
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
