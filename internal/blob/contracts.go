// safety: Package blob defines verified bytes and authorized reference edges.
// Credential checks belong to C2 and transactional persistence belongs to pkg/store.
package blob

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// safety: Digest names immutable bytes by lowercase SHA-256.
type Digest string

// safety: Kind excludes logs because log reads must be controller-proxied.
type Kind string

const (
	KindOutput       Kind = "output"
	KindManifest     Kind = "manifest"
	KindMemo         Kind = "memo"
	KindDepcache     Kind = "depcache"
	KindBinary       Kind = "binary"
	KindSourceBundle Kind = "source_bundle"
)

// safety: Kinds is the complete record-kind registry for schema and wire drift checks.
var Kinds = []Kind{KindOutput, KindManifest, KindMemo, KindDepcache, KindBinary, KindSourceBundle}

// safety: Blob is existence truth; object storage alone cannot establish a live blob.
type Blob struct {
	Team      string
	Digest    Digest
	Size      int64
	CreatedAt time.Time
}

// safety: Provenance is assigned by the server, never copied from upload metadata.
type Provenance string

const (
	ProvenanceCloud Provenance = "cloud"
	ProvenanceLocal Provenance = "local"
)

// safety: Principal is an opaque server-resolved C2 reference, never a bearer token.
type Principal string

// safety: RefScope distinguishes submitter-vouched refs from verified refs.
type RefScope struct {
	Ref      string
	Verified bool
}

// safety: RefFacts carries server-sealed evidence rather than accepting client trust flags.
type RefFacts struct {
	Scope                 RefScope
	DefaultBranch         string
	DefaultBranchVerified bool
	AppDeliveryKey        string
	SHA                   string
	PullRequestBase       runspec.Optional[string]
}

// safety: Scope bounds records by authorized repository, ref and pipeline.
type Scope struct {
	Repo     runspec.RepoKey
	Ref      RefScope
	Pipeline string
	Job      string
	Key      string
}

// safety: RecordKey keeps the kind, server-derived namespace and logical name distinct.
type RecordKey struct {
	Team  string
	Kind  Kind
	Scope Scope
	Name  string
}

// safety: Record binds source facts and provenance to immutable committed bytes.
type Record struct {
	Key            RecordKey
	Digest         Digest
	ManifestDigest runspec.Optional[Digest]
	Uploader       Principal
	Source         UploadIdentity
	Provenance     Provenance
	RefFacts       RefFacts
	CreatedAt      time.Time
	ExpiresAt      runspec.Optional[time.Time]
	Pinned         bool
}

// safety: Member makes every manifest edge independently subject to authority checks.
type Member struct {
	Team           string
	ManifestDigest Digest
	Digest         Digest
	Path           string
	Size           int64
	Mode           uint32
}

// safety: Receipt retains its tagged uploader and cannot become run-wide job authority.
// Job and imported-publication receipts keep the exact job, attempt and generation.
type Receipt struct {
	Identity  UploadIdentity
	Digest    Digest
	CreatedAt time.Time
}

// safety: Tombstone starts reachability GC's grace period, never an age-only object expiry.
type Tombstone struct {
	Team        string
	Digest      Digest
	MarkedAt    time.Time
	DeleteAfter time.Time
}

// safety: EdgeKind enumerates the only admitted ways to make bytes reachable.
type EdgeKind string

const (
	EdgeOwnUpload         EdgeKind = "own_upload"
	EdgeDependency        EdgeKind = "dependency"
	EdgeDeclaredReference EdgeKind = "declared_reference"
	EdgeMemoHit           EdgeKind = "memo_hit"
	EdgeMemoScope         EdgeKind = "memo_scope"
)

// safety: EdgeKinds is complete so authorization tests cannot silently omit an edge.
var EdgeKinds = []EdgeKind{EdgeOwnUpload, EdgeDependency, EdgeDeclaredReference, EdgeMemoHit, EdgeMemoScope}

// safety: ReadEdge records a server-derived ancestry or scoped-cache entitlement.
type ReadEdge struct {
	Kind   EdgeKind
	Reader runspec.AttemptIdentity
	Source runspec.AttemptIdentity
	Record RecordKey
	Digest Digest
}

// safety: ReadSet is a transaction snapshot and must never be decoded from caller JSON.
type ReadSet struct {
	Reader runspec.AttemptIdentity
	Edges  []ReadEdge
}

// safety: Authority carries opaque C2 references for a transaction-time authorization recheck.
// Possession of this struct is not proof of authority; all values must be resolved server-side.
type Authority struct {
	CapabilityRef string
	Principal     Principal
	Attempt       runspec.Optional[runspec.AttemptIdentity]
	Repo          runspec.RepoKey
	Ref           RefFacts
	// Upload binds owner and publication writes, which have no job attempt.
	Upload runspec.Optional[UploadIdentity]
}

// safety: EdgeRequest checks authority independently from integrity and existence.
type EdgeRequest struct {
	TargetKind Kind
	Authority  Authority
	Digest     Digest
	ReadSet    ReadSet
	OwnReceipt runspec.Optional[Receipt]
}

// safety: AuthorizeEdge rejects unrelated same-run receipts as digest_not_readable.
// Own-upload job edges require the exact Attempt, including generation. Owner and non-job
// publication receipts authorize only binary/source_bundle, never another job's outputs.
// Imported publication output edges require the matching imported attempt in a publish run.
func AuthorizeEdge(request EdgeRequest) error { return authorizeEdge(request) }

// safety: CommitRecordRequest leaves provenance to server derivation and checks every member.
type CommitRecordRequest struct {
	Source         UploadIdentity
	Authority      Authority
	Key            RecordKey
	Digest         Digest
	ManifestDigest runspec.Optional[Digest]
	Members        []Member
	ExpiresAt      runspec.Optional[time.Time]
}

// safety: RecordStore methods execute with live binding, existence and edge checks in one transaction.
type RecordStore interface {
	LookupRecord(context.Context, RecordLookup) (RecordLookupResult, error)
	LookupMemo(context.Context, MemoLookup) (MemoHit, error)
	LookupDepcache(context.Context, DepcacheLookup) (DepcacheHit, error)
	CommitRecord(context.Context, CommitRecordRequest) (Record, error)
	CopyMemo(context.Context, CopyMemoRequest) (MemoHit, error)
}

// safety: Budget bounds a batched existence query by an explicit caller deadline.
// No package default silently adds a customer-facing limit; limits owns policy values.
type Budget struct{ Deadline time.Time }

// safety: PutRequest signs both expected size and digest before any transfer.
type PutRequest struct {
	Identity  UploadIdentity
	Authority Authority
	Digest    Digest
	Size      int64
	Body      io.Reader
}

// safety: GetRequest names bytes only through a live server-computed read set.
type GetRequest struct {
	Authority Authority
	Digest    Digest
}

// safety: HasManyRequest must never expose existence beyond the caller's read set.
type HasManyRequest struct {
	Authority Authority
	Digests   []Digest
	Budget    Budget
}

// safety: Presence reports only readable, live blobs; absent and unauthorized are indistinguishable.
type Presence struct{ Present []Digest }

// safety: VerifiedReader reports digest mismatch at EOF; a caller must consume EOF for success.
// Close before EOF does not claim verification.
type VerifiedReader interface {
	io.ReadCloser
	Verified() bool
}

// safety: Store is the sole byte backend: signed /data upload plus commit, and verified downloads.
// Put yields a receipt only after size/digest verification and immutable-key commit.
type Store interface {
	Put(context.Context, PutRequest) (Receipt, error)
	Get(context.Context, GetRequest) (VerifiedReader, error)
	HasMany(context.Context, HasManyRequest) (Presence, error)
}

// safety: SignedTransfer carries a one-object external URL, not a Sparkwing credential.
type SignedTransfer struct {
	URL       string
	Headers   map[string]string
	ExpiresAt time.Time
	Digest    Digest
	Size      int64
}

// safety: UploadIntent binds staging bytes to the uploader before issuing a PUT URL.
type UploadIntent struct {
	Identity  UploadIdentity
	Authority Authority
	Digest    Digest
	Size      int64
}

// safety: UploadTicket is opaque and is checked again by the commit endpoint.
type UploadTicket struct {
	ID       string
	Transfer SignedTransfer
}

// safety: CommitUpload cannot substitute a digest or uploader after signing.
type CommitUpload struct {
	Authority Authority
	TicketID  string
}

// safety: DataSigner is the controller's signed-data backend contract for S3 or filesystem.
// Download expiry comes from limits (60 seconds), and logs use a separate signer.
type DataSigner interface {
	Upload(context.Context, UploadIntent) (UploadTicket, error)
	Commit(context.Context, CommitUpload) (Receipt, error)
	Download(context.Context, GetRequest) (SignedTransfer, error)
	HasMany(context.Context, HasManyRequest) (Presence, error)
}

// safety: SignedStoreOptions injects signed-data transport without adding another cache protocol.
type SignedStoreOptions struct {
	Signer     DataSigner
	HTTPClient HTTPClient
}

// safety: HTTPClient constrains transport injection to one signed transfer operation.
type HTTPClient interface {
	Transfer(context.Context, SignedTransfer, io.Reader) (io.ReadCloser, error)
}

// safety: NewSignedStore verifies transferred bytes and never falls back to cache-service routes.
func NewSignedStore(options SignedStoreOptions) Store { return newSignedStore(options) }

// safety: ObjectKey produces teams/<team>/cas/<sha256> after validating both components.
func ObjectKey(team string, digest Digest) (string, error) { return objectKey(team, digest) }

// safety: MemoKey cannot share an unclaimed result across pipelines.
type MemoKey struct {
	Repo     runspec.RepoKey
	Ref      RefScope
	Pipeline string
	Job      string
	Hash     runspec.Hash
}

// safety: MemoNamespace reserves manual:<ref> for submitter-vouched writes.
func MemoNamespace(key MemoKey) (string, error) { return memoNamespace(key) }

// safety: MemoLookup uses the authority's server-derived scope and verified default fallback.
type MemoLookup struct {
	Authority Authority
	Key       MemoKey
	Budget    Budget
}

// safety: CopyMemoRequest creates output and manifest edges atomically with the hit.
type CopyMemoRequest struct {
	Lookup      MemoLookup
	Destination runspec.AttemptIdentity
}

// safety: MemoHit carries digests and authorized edges rather than relying on the source run's survival.
type MemoHit struct {
	Found    bool
	Output   runspec.Optional[Digest]
	Manifest runspec.Optional[Digest]
	Members  []Member
	ReadSet  ReadSet
}

// safety: DepcacheKey shares repo/ref isolation with memo while naming a file cache.
type DepcacheKey struct {
	Repo runspec.RepoKey
	Ref  RefScope
	Name string
	Key  string
}

// safety: RestoreSource orders own-ref, PR-base and verified-default cache lookups.
type RestoreSource string

const (
	RestoreOwnRef        RestoreSource = "own_ref"
	RestorePRBase        RestoreSource = "pr_base"
	RestoreDefaultBranch RestoreSource = "default_branch"
)

// safety: RestoreRule makes the trusted fallback requirement explicit.
type RestoreRule struct {
	Source                    RestoreSource
	RequireVerifiedProvenance bool
}

// safety: DepcacheRestoreOrder is the complete ordered restore policy.
var DepcacheRestoreOrder = []RestoreRule{{RestoreOwnRef, false}, {RestorePRBase, true}, {RestoreDefaultBranch, true}}

// safety: DepcacheLookup never accepts a caller-selected repo outside its data grant.
type DepcacheLookup struct {
	Authority Authority
	Key       DepcacheKey
	Budget    Budget
}

// safety: FailureUse distinguishes safe cold starts from missing required bytes.
type FailureUse string

const (
	UseMemo     FailureUse = "memo"
	UseStaging  FailureUse = "staging"
	UseOutput   FailureUse = "output"
	UseDepcache FailureUse = "depcache"
)

// safety: FailureAction prevents missing bytes or exhausted budgets from looking successful.
type FailureAction string

const (
	FailureMiss  FailureAction = "miss"
	FailureError FailureAction = "error"
	FailureCold  FailureAction = "cold"
)

// safety: FailureRule applies equally to missing, corrupt and over-budget reads.
type FailureRule struct {
	Use    FailureUse
	Action FailureAction
}

// safety: FailureRules is the complete per-use failure policy.
var FailureRules = []FailureRule{{UseMemo, FailureMiss}, {UseStaging, FailureError}, {UseOutput, FailureError}, {UseDepcache, FailureCold}}

// safety: BuildTrust preserves the team-owned opt-in for local binary provenance.
type BuildTrust struct {
	Team             string
	TrustLocalBuilds bool
}

// safety: BinaryExecution binds cloud execution to the committed digest and provenance.
type BinaryExecution struct {
	Record Record
	Trust  BuildTrust
	Digest Digest
}

// safety: CheckBinaryExecution permits local provenance only with trust_local_builds.
func CheckBinaryExecution(execution BinaryExecution) error { return checkBinaryExecution(execution) }

// safety: RemoteCacheOptions uses the user's existing repo-bound read authority.
type RemoteCacheOptions struct {
	Profile      string
	AuthorityRef string
	Repo         runspec.RepoKey
	Records      RecordStore
}

// safety: RemoteCache exposes reads only, with no binary fetch or shared-cache write method.
// Every returned record must originate in a verified default-branch run.
type RemoteCache interface {
	Memo(context.Context, MemoKey, Budget) (MemoHit, error)
	Depcache(context.Context, DepcacheKey, Budget) (Record, error)
	Artifact(context.Context, Digest) (VerifiedReader, error)
}

// safety: OpenRemoteCache is opt-in by profile and never borrows a runner credential.
func OpenRemoteCache(options RemoteCacheOptions, store Store) (RemoteCache, error) {
	return openRemoteCache(options, store)
}

// safety: PublishPolicy labels published results local and never creates an owner claim.
// Publication failure is reported separately from the completed local run's outcome.
type PublishPolicy struct {
	Profile           string
	AuthorityRef      string
	LocalRunID        string
	ExecutionLocation string
	Trust             BuildTrust
}

// safety: ErrBudget follows FailureRules instead of being treated as successful absence.
var ErrBudget = errors.New("blob budget exhausted")

// safety: ErrNotFound makes missing committed bytes loud at required-read boundaries.
var ErrNotFound = errors.New("blob not found")

// safety: ErrIntegrity prevents corrupt bytes from being accepted as a successful read.
var ErrIntegrity = errors.New("blob integrity mismatch")

// safety: ErrDigestNotReadable maps through C2 to forbidden with reason digest_not_readable.
var ErrDigestNotReadable = errors.New("digest_not_readable")

// safety: RecordLookup combines a logical key with authority instead of accepting a bare CAS digest.
// Local stores may use local repository keys. Controller lookups require repo-bound data authority.
type RecordLookup struct {
	Authority Authority
	Key       RecordKey
	Budget    Budget
	// VerifiedDefaultOnly resolves the ref from sealed default-branch evidence.
	VerifiedDefaultOnly bool
}

// safety: RecordLookupResult distinguishes a cache miss from a record with required missing bytes.
// All returned members have passed existence and edge checks before their digests enter ReadSet.
type RecordLookupResult struct {
	Found   bool
	Record  Record
	Members []Member
	ReadSet ReadSet
}

// safety: DepcacheHit records the winning fallback so provenance remains visible after a restore.
// No hit, corrupt bytes and exhausted budget mean a cold cache under FailureRules.
type DepcacheHit struct {
	Found   bool
	Record  Record
	Source  RestoreSource
	ReadSet ReadSet
}

// safety: WriteScopeIntent omits repo, ref and trust because the authority supplies those facts.
// Pipeline/job choices still require membership checks against the sealed run at commit.
type WriteScopeIntent struct {
	Kind     Kind
	Pipeline string
	Job      string
	Key      string
	Name     string
}

// safety: ResolveWriteKey derives repository and vouched/manual ref scope from live authority.
// It rejects client-selected real-ref writes without server-verified facts.
func ResolveWriteKey(authority Authority, intent WriteScopeIntent) (RecordKey, error) {
	return resolveWriteKey(authority, intent)
}

// safety: RecordNamespace encodes typed components without delimiter collisions.
// Memo namespaces preserve memo/<repo_key>/<ref_scope>/<pipeline>/<job>/<hash> semantics.
func RecordNamespace(key RecordKey) (string, error) { return recordNamespace(key) }

// safety: DepcacheNamespace shares the memo write-ref rule while preserving the cache name and key.
func DepcacheNamespace(key DepcacheKey) (string, error) { return depcacheNamespace(key) }

// safety: UploadIdentityKind covers executable jobs, preplan owners and non-executable publication.
type UploadIdentityKind string

const (
	UploadJob         UploadIdentityKind = "job"
	UploadOwnerUnit   UploadIdentityKind = "owner_unit"
	UploadPublication UploadIdentityKind = "publication"
)

// safety: UploadUnitKind keeps owner uploads bound to the live awarded unit's fence.
type UploadUnitKind string

const (
	UploadTriggerUnit UploadUnitKind = "trigger"
	UploadPlanUnit    UploadUnitKind = "plan"
	UploadEvalUnit    UploadUnitKind = "eval"
)

// safety: UploadUnit identifies the owner lease without inventing a job before AcceptPlan.
// Eval source identity binds the evaluation to its terminal generator source.
type UploadUnit struct {
	Kind       UploadUnitKind
	ID         string
	Generation int64
	Source     runspec.Optional[runspec.AttemptIdentity]
}

// safety: UploadIdentity is resolved by C2 from live authority, never accepted as caller proof.
// Job requires Attempt and forbids Unit. Owner requires Unit and forbids Attempt.
// Publication forbids Unit and requires a publish-mode run without an executable claim.
// Imported publication outputs require Attempt matching that run's imported job result.
// Nested attempt Team/RunID must equal this identity's Team/RunID.
type UploadIdentity struct {
	Kind          UploadIdentityKind
	Team          string
	RunID         string
	Principal     Principal
	CapabilityRef string
	Attempt       runspec.Optional[runspec.AttemptIdentity]
	Unit          runspec.Optional[UploadUnit]
}

// safety: UploadRule distinguishes non-job byte intake from job-owned output edges.
type UploadRule struct {
	IdentityKind       UploadIdentityKind
	RequiresAttempt    bool
	RequiresUnit       bool
	RequiresPublishRun bool
	RecordKinds        []Kind
}

// safety: UploadRules is the complete identity/record-kind policy, rechecked at commit.
// Publication with an imported attempt follows the final row rather than non-job publication.
var UploadRules = []UploadRule{
	{UploadJob, true, false, false, []Kind{KindOutput, KindManifest, KindMemo, KindDepcache, KindBinary, KindSourceBundle}},
	{UploadOwnerUnit, false, true, false, []Kind{KindBinary, KindSourceBundle}},
	{UploadPublication, false, false, true, []Kind{KindBinary, KindSourceBundle}},
	{UploadPublication, true, false, true, []Kind{KindOutput, KindManifest}},
}

// safety: AuthorizeUpload checks the C2-resolved identity and target kind in the write transaction.
// It requires a live owner fence or publication authority, verifies imported job identity,
// and rejects identity/principal/capability mismatches with the authenticated authority.
func AuthorizeUpload(authority Authority, identity UploadIdentity, kind Kind) error {
	return authorizeUpload(authority, identity, kind)
}
