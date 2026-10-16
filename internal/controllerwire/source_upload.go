package controllerwire

import (
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// SourceUploadRequest binds the snapshot to the requested repository and revisions.
type SourceUploadRequest struct {
	Git    runspec.Git    `json:"git"`
	Source runspec.Source `json:"source"`
	Digest blob.Digest    `json:"digest"`
	Size   int64          `json:"size"`
}
