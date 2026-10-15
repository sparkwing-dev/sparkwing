package storage

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BlobManifests supplies verified physical members to the store's authorized record transaction.
type BlobManifests struct{ Objects blob.ObjectBackend }

// ReadBlobManifest reads the tenant and digest selected by the record transaction.
func (m BlobManifests) ReadBlobManifest(ctx context.Context, team store.Team, digest blob.Digest) ([]blob.Member, error) {
	if m.Objects == nil {
		return nil, fmt.Errorf("manifest reader requires an object backend")
	}
	return m.Objects.ReadManifest(ctx, string(team), digest)
}

var _ store.BlobManifestReader = BlobManifests{}
