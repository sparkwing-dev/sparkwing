package controllerwire

import "github.com/sparkwing-dev/sparkwing/internal/blob"

// ArtifactPage is one bounded page of committed files without download credentials.
type ArtifactPage struct {
	Artifacts  []blob.ManifestEntry `json:"artifacts"`
	NextCursor string               `json:"next_cursor"`
	Truncated  bool                 `json:"truncated"`
}

// JobArtifacts authorizes one exact committed file download.
type JobArtifacts struct {
	Artifacts []blob.ManifestEntry `json:"artifacts"`
	Download  *blob.SignedTransfer `json:"download"`
}
