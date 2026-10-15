package blob

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// ObjectBackend requires verified ticket-specific staging bytes, including when the final blob already exists.
type ObjectBackend interface {
	capability.DataSigner
	CommitObject(context.Context, string, Blob) error
	DeleteObject(context.Context, string) error
}
