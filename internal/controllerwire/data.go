package controllerwire

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// DataUploadRequest selects bytes; the controller derives uploader and scope.
type DataUploadRequest struct {
	Digest blob.Digest `json:"digest"`
	Size   int64       `json:"size"`
}

// DataUploadTicket grants a signed upload to one immutable object.
type DataUploadTicket struct {
	UploadID  string            `json:"upload_id"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	SHA256    blob.Digest       `json:"sha256"`
	Size      int64             `json:"size"`
}

// DataCommitRequest selects a persisted upload ticket under current authority.
type DataCommitRequest struct {
	UploadID string `json:"upload_id"`
}

// DataReceipt acknowledges verified bytes committed under the authenticated uploader.
type DataReceipt struct {
	Digest    blob.Digest `json:"digest"`
	CreatedAt time.Time   `json:"created_at"`
}

// DataDownloadRequest selects one authorized digest.
type DataDownloadRequest struct {
	Digest blob.Digest `json:"digest"`
}

// DataSignedTransfer carries the native controller's one-object download response.
type DataSignedTransfer struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires"`
	Digest    blob.Digest       `json:"sha256"`
	Size      int64             `json:"size"`
}

// DataHasManyRequest queries authorized presence under an optional budget.
type DataHasManyRequest struct {
	Digests  []blob.Digest `json:"digests"`
	Deadline *time.Time    `json:"deadline,omitempty"`
}

// DataPresence contains only digests requested and authorized by this caller.
type DataPresence struct {
	Present []blob.Digest `json:"present"`
}

// DataMember identifies one record member without transferring reader authority.
type DataMember struct {
	Digest blob.Digest `json:"digest"`
	Path   string      `json:"path"`
	Size   int64       `json:"size"`
	Mode   uint32      `json:"mode"`
}

// DataRecord describes a canonical record selected under current authority.
type DataRecord struct {
	Kind           blob.Kind       `json:"kind"`
	Repo           runspec.RepoKey `json:"repo"`
	Ref            string          `json:"ref"`
	Pipeline       string          `json:"pipeline"`
	Job            string          `json:"job"`
	Key            string          `json:"key"`
	Name           string          `json:"name"`
	Digest         blob.Digest     `json:"digest"`
	ManifestDigest *blob.Digest    `json:"manifest_digest,omitempty"`
	Provenance     blob.Provenance `json:"provenance"`
	CreatedAt      time.Time       `json:"created_at"`
	ExpiresAt      *time.Time      `json:"expires_at,omitempty"`
}

// DataRecordLookupResult carries a found record and its persisted members.
type DataRecordLookupResult struct {
	Found           bool         `json:"found"`
	VerifiedDefault bool         `json:"verified_default,omitempty"`
	Record          *DataRecord  `json:"record,omitempty"`
	Members         []DataMember `json:"members"`
}

// DataMemoLookupRequest selects a memo within the authenticated repository.
type DataMemoLookupRequest struct {
	Pipeline string       `json:"pipeline"`
	Job      string       `json:"job"`
	Hash     runspec.Hash `json:"hash"`
	Deadline *time.Time   `json:"deadline,omitempty"`
}

// DataMemoLookupResult carries result references and the caller's reader fence.
type DataMemoLookupResult struct {
	Found    bool                     `json:"found"`
	Output   *blob.Digest             `json:"output,omitempty"`
	Manifest *blob.Digest             `json:"manifest,omitempty"`
	Members  []DataMember             `json:"members"`
	Reader   *runspec.AttemptIdentity `json:"reader,omitempty"`
}

// DataDepcacheLookupRequest selects a declared cache within authenticated execution storage.
type DataDepcacheLookupRequest struct {
	Name     string     `json:"name"`
	Key      string     `json:"key"`
	Deadline *time.Time `json:"deadline,omitempty"`
}

// DataDepcacheLookupResult carries an authorized repository cache selection.
type DataDepcacheLookupResult struct {
	Found  bool               `json:"found"`
	Record *DataRecord        `json:"record,omitempty"`
	Source blob.RestoreSource `json:"source,omitempty"`
}

// DataRecordWriteRequest commits logical record selectors under server-derived scope.
type DataRecordWriteRequest struct {
	Kind           blob.Kind    `json:"kind"`
	Pipeline       string       `json:"pipeline"`
	Job            string       `json:"job"`
	Key            string       `json:"key"`
	Name           string       `json:"name"`
	Digest         blob.Digest  `json:"digest"`
	ManifestDigest *blob.Digest `json:"manifest_digest,omitempty"`
	Members        []DataMember `json:"members"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
}
