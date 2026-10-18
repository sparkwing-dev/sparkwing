package storage

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// BlobManifests verifies physical manifests for authorized store record calls.
type BlobManifests struct{ Objects blob.ObjectBackend }

// VerifyBlobManifest verifies the tenant, digest and members a record commit names.
func (m BlobManifests) VerifyBlobManifest(ctx context.Context, team store.Team, digest blob.Digest, members []blob.Member) error {
	if m.Objects == nil {
		return fmt.Errorf("manifest reader requires an object backend")
	}
	return m.Objects.VerifyManifest(ctx, string(team), digest, members)
}

// ReadBlobManifest reads admitted members for authorized record lookup.
func (m BlobManifests) ReadBlobManifest(ctx context.Context, team store.Team, digest blob.Digest) ([]blob.Member, error) {
	if m.Objects == nil {
		return nil, fmt.Errorf("manifest reader requires an object backend")
	}
	return m.Objects.ReadManifest(ctx, string(team), digest)
}

// VerifyBlob verifies selected result bytes before a materialized copy.
func (m BlobManifests) VerifyBlob(ctx context.Context, expected blob.Blob) error {
	if m.Objects == nil {
		return fmt.Errorf("result copy requires an object backend")
	}
	return m.Objects.VerifyBlob(ctx, expected)
}

var _ store.BlobManifestReader = BlobManifests{}
