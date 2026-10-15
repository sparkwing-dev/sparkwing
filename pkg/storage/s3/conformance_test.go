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
