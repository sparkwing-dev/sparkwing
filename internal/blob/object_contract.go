package blob

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ObjectBackend owns verified publication, tenant manifest reads and physical deletion.
type ObjectBackend interface {
	capability.DataSigner
	CommitObject(context.Context, string, Blob) error
	DeleteObject(context.Context, string) error
	DeleteBlob(context.Context, Blob) error
	ReadManifest(context.Context, string, Digest) ([]Member, error)
	VerifyBlob(context.Context, Blob) error
}
