package blob

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ObjectBackend signs authorized transfers and publishes verified immutable bytes.
// CommitObject takes a server-derived staging key and the expected final blob.
// It verifies exact size and SHA-256, including an already-present final object,
// before returning success. The controller reauthorizes before publication and
// the store rechecks authority when committing existence and the uploader receipt.
// A physical object alone grants neither existence nor read authority.
type ObjectBackend interface {
	capability.DataSigner
	CommitObject(context.Context, string, Blob) error
	DeleteObject(context.Context, string) error
}
