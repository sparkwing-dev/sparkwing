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

// RepositoryResolveRequest asks the controller to resolve a clone URL through team source access.
type RepositoryResolveRequest struct {
	RemoteURL string `json:"remote_url"`
}
