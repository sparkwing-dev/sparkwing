package controllerwire

import "github.com/sparkwing-dev/sparkwing/internal/blob"

// JobArtifacts lists committed files and optionally authorizes one exact file download.
type JobArtifacts struct {
	Artifacts []blob.ManifestEntry `json:"artifacts"`
	Download  *blob.SignedTransfer `json:"download,omitempty"`
}
