package capability

import (
	"context"
	"time"
)

// DataUse constrains an opt-in separately from an executable claim's data authority.
type DataUse string

const (
	DataClaim       DataUse = "claim"
	DataRemoteCache DataUse = "remote_cache"
	DataPublish     DataUse = "publish"
	DataExpansion   DataUse = "expansion"
)

// DataScope comes from sealed source facts or existing repo authority, never a typed repository.
// A vouched ref's WriteRef is manual:<ref>. Verified fallback reads require verified record provenance.
type DataScope struct {
	Use                 DataUse  `json:"use"`
	RepositoryKey       string   `json:"repo_key"`
	WriteRef            string   `json:"write_ref,omitempty"`
	ReadRefs            []string `json:"read_refs"`
	VerifiedDefaultOnly bool     `json:"verified_default_only"`
	RunID               string   `json:"run_id,omitempty"`
	NodeID              string   `json:"job_id,omitempty"`
	Attempt             int64    `json:"attempt,omitempty"`
	Generation          int64    `json:"generation,omitempty"`
}

// DataOperation names each authorization point, including commit-time reauthorization.
type DataOperation string

const (
	DataDownload DataOperation = "download"
	DataUpload   DataOperation = "upload"
	DataCommit   DataOperation = "commit"
	DataHasMany  DataOperation = "has_many"
	DataRecord   DataOperation = "record"
	DataLookup   DataOperation = "lookup"
)

// DataPolicy keeps the signing path from treating blob existence as authority.
type DataPolicy struct {
	Operation           DataOperation
	Grant               Grant
	RequireReadSet      bool
	RequireUploader     bool
	CheckReferenceEdges bool
}

// DataPolicies covers all object operations; logs use their separate signed PUT path.
var DataPolicies = []DataPolicy{
	{DataDownload, DataRead, true, false, false},
	{DataUpload, DataWrite, false, true, false},
	{DataCommit, DataWrite, false, true, false},
	{DataHasMany, DataRead, true, false, false},
	{DataRecord, DataWrite, false, false, true},
	{DataLookup, DataRead, true, false, false},
}

// DataTarget carries selectors for C1's read-set and receipt checks, not asserted provenance.
type DataTarget struct {
	Operation   DataOperation
	Digest      string
	UploadID    string
	RunID       string
	NodeID      string
	Attempt     int64
	Generation  int64
	RecordKind  string
	RecordScope string
	RecordName  string
}

// DataAccessChecker is implemented by C1's blob authority owner, including receipt and read-set checks.
// It resolves live records and refuses references between unrelated jobs even within one run.
type DataAccessChecker interface {
	CheckDataAccess(context.Context, Authorized, DataTarget) error
}

// AuthorizeData requires route authority, the sealed data scope and C1's edge checks before signing.
// Commit and record writes repeat these checks in the transaction that creates the receipt or edge.
func AuthorizeData(ctx context.Context, edges DataAccessChecker, authorized Authorized, target DataTarget) error {
	panic("unimplemented: c2-credentials")
}

// ObjectToSign is constructed after authorization and quota reservation, with a server-derived key.
// PUTs bind Size and SHA256; GETs name one readable object and exclude logs.
type ObjectToSign struct {
	Method    string
	Key       string
	Size      int64
	SHA256    string
	ExpiresAt time.Time
}

// SignedObject is an external output, not another capability or a revocable credential.
type SignedObject struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	SHA256    string            `json:"sha256,omitempty"`
	Size      int64             `json:"size"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// DataSigner gives S3/CloudFront and filesystem storage the same authorized signing boundary.
// Data GETs expire after 60 seconds; log PUTs after 15 minutes, as owned by internal/limits.
type DataSigner interface {
	Sign(context.Context, ObjectToSign) (SignedObject, error)
}
