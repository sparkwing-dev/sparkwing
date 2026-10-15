package controllerwire

import (
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// DataAuthoritySelector carries comparisons against authenticated state. Ref
// facts and read sets are absent because only the server can establish them.
type DataAuthoritySelector struct {
	CapabilityRef string                   `json:"capability_ref"`
	Principal     blob.Principal           `json:"principal"`
	Attempt       *runspec.AttemptIdentity `json:"attempt,omitempty"`
	Repo          runspec.RepoKey          `json:"repo"`
	Upload        *DataUploadIdentity      `json:"upload,omitempty"`
}

type DataUploadUnit struct {
	Kind       blob.UploadUnitKind                       `json:"kind"`
	ID         string                                    `json:"id"`
	Generation int64                                     `json:"generation"`
	Source     runspec.Optional[runspec.AttemptIdentity] `json:"source"`
}

// DataUploadIdentity selects the uploader; possession confers no authority.
type DataUploadIdentity struct {
	Kind          blob.UploadIdentityKind                   `json:"kind"`
	Team          string                                    `json:"team"`
	RunID         string                                    `json:"run_id"`
	Principal     blob.Principal                            `json:"principal"`
	CapabilityRef string                                    `json:"capability_ref"`
	Attempt       runspec.Optional[runspec.AttemptIdentity] `json:"attempt"`
	Unit          runspec.Optional[DataUploadUnit]          `json:"unit"`
}

type DataRefScope struct {
	Ref      string `json:"ref"`
	Verified bool   `json:"verified"`
}

type DataRefFacts struct {
	Scope                 DataRefScope             `json:"scope"`
	DefaultBranch         string                   `json:"default_branch"`
	DefaultBranchVerified bool                     `json:"default_branch_verified"`
	AppDeliveryKey        string                   `json:"app_delivery_key"`
	SHA                   string                   `json:"sha"`
	PullRequestBase       runspec.Optional[string] `json:"pull_request_base"`
}

type DataScope struct {
	Repo     runspec.RepoKey `json:"repo"`
	Ref      DataRefScope    `json:"ref"`
	Pipeline string          `json:"pipeline"`
	Job      string          `json:"job"`
	Key      string          `json:"key"`
}

type DataRecordKey struct {
	Team  string    `json:"team"`
	Kind  blob.Kind `json:"kind"`
	Scope DataScope `json:"scope"`
	Name  string    `json:"name"`
}

type DataRecord struct {
	Key            DataRecordKey                 `json:"key"`
	Digest         blob.Digest                   `json:"digest"`
	ManifestDigest runspec.Optional[blob.Digest] `json:"manifest_digest"`
	Uploader       blob.Principal                `json:"uploader"`
	Source         DataUploadIdentity            `json:"source"`
	Provenance     blob.Provenance               `json:"provenance"`
	RefFacts       DataRefFacts                  `json:"ref_facts"`
	CreatedAt      time.Time                     `json:"created_at"`
	ExpiresAt      runspec.Optional[time.Time]   `json:"expires_at"`
	Pinned         bool                          `json:"pinned"`
}

type DataMember struct {
	Team           string      `json:"team"`
	ManifestDigest blob.Digest `json:"manifest_digest"`
	Digest         blob.Digest `json:"digest"`
	Path           string      `json:"path"`
	Size           int64       `json:"size"`
	Mode           uint32      `json:"mode"`
}

type DataReadEdge struct {
	Kind   blob.EdgeKind           `json:"kind"`
	Reader runspec.AttemptIdentity `json:"reader"`
	Source runspec.AttemptIdentity `json:"source"`
	Record DataRecordKey           `json:"record"`
	Digest blob.Digest             `json:"digest"`
}

// DataReadSet is response metadata. A server must never accept it as request proof.
type DataReadSet struct {
	Reader runspec.AttemptIdentity `json:"reader"`
	Edges  []DataReadEdge          `json:"edges"`
}

type DataSignedTransfer struct {
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	Digest    blob.Digest       `json:"digest"`
	Size      int64             `json:"size"`
}

type DataUploadTicket struct {
	ID       string             `json:"id"`
	Transfer DataSignedTransfer `json:"transfer"`
}

type DataReceipt struct {
	Identity  DataUploadIdentity `json:"identity"`
	Digest    blob.Digest        `json:"digest"`
	CreatedAt time.Time          `json:"created_at"`
}

type DataUploadRequest struct {
	Authority DataAuthoritySelector `json:"authority"`
	Identity  DataUploadIdentity    `json:"identity"`
	Digest    blob.Digest           `json:"digest"`
	Size      int64                 `json:"size"`
}

type DataCommitRequest struct {
	Authority DataAuthoritySelector `json:"authority"`
	TicketID  string                `json:"ticket_id"`
}

type DataDownloadRequest struct {
	Authority DataAuthoritySelector `json:"authority"`
	Digest    blob.Digest           `json:"digest"`
}

type DataHasManyRequest struct {
	Authority DataAuthoritySelector `json:"authority"`
	Digests   []blob.Digest         `json:"digests"`
	Deadline  *time.Time            `json:"deadline,omitempty"`
}

type DataPresence struct {
	Present []blob.Digest `json:"present"`
}

type DataMemoKey struct {
	Repo     runspec.RepoKey `json:"repo"`
	Ref      DataRefScope    `json:"ref"`
	Pipeline string          `json:"pipeline"`
	Job      string          `json:"job"`
	Hash     runspec.Hash    `json:"hash"`
}

type DataDepcacheKey struct {
	Repo runspec.RepoKey `json:"repo"`
	Ref  DataRefScope    `json:"ref"`
	Name string          `json:"name"`
	Key  string          `json:"key"`
}

// DataLookupRequest is JSON in the selector query parameter of GET /data/records.
// Lookup selects exactly one of record, memo or depcache. VerifiedDefaultOnly is
// explicit for record lookups; authenticated remote-cache scope also forces it.
type DataLookupRequest struct {
	Authority           DataAuthoritySelector `json:"authority"`
	Record              *DataRecordKey        `json:"record,omitempty"`
	Memo                *DataMemoKey          `json:"memo,omitempty"`
	Depcache            *DataDepcacheKey      `json:"depcache,omitempty"`
	Deadline            *time.Time            `json:"deadline,omitempty"`
	VerifiedDefaultOnly bool                  `json:"verified_default_only"`
}

type DataRecordLookupResult struct {
	Found   bool         `json:"found"`
	Record  DataRecord   `json:"record"`
	Members []DataMember `json:"members"`
	ReadSet DataReadSet  `json:"read_set"`
}

type DataMemoHit struct {
	Found    bool                          `json:"found"`
	Output   runspec.Optional[blob.Digest] `json:"output"`
	Manifest runspec.Optional[blob.Digest] `json:"manifest"`
	Members  []DataMember                  `json:"members"`
	ReadSet  DataReadSet                   `json:"read_set"`
}

type DataDepcacheHit struct {
	Found   bool               `json:"found"`
	Record  DataRecord         `json:"record"`
	Source  blob.RestoreSource `json:"source"`
	ReadSet DataReadSet        `json:"read_set"`
}

type DataRecordCommit struct {
	Source         DataUploadIdentity            `json:"source"`
	Authority      DataAuthoritySelector         `json:"authority"`
	Key            DataRecordKey                 `json:"key"`
	Digest         blob.Digest                   `json:"digest"`
	ManifestDigest runspec.Optional[blob.Digest] `json:"manifest_digest"`
	Members        []DataMember                  `json:"members"`
	ExpiresAt      runspec.Optional[time.Time]   `json:"expires_at"`
}

type DataMemoCopy struct {
	Lookup      DataLookupRequest       `json:"lookup"`
	Destination runspec.AttemptIdentity `json:"destination"`
}

// DataRecordWriteRequest tags POST /data/records as commit or copy_memo. Exactly
// the matching payload is present; CopyMemo installs edges atomically at C1.
type DataRecordWriteRequest struct {
	Operation string            `json:"operation"`
	Commit    *DataRecordCommit `json:"commit,omitempty"`
	CopyMemo  *DataMemoCopy     `json:"copy_memo,omitempty"`
}
