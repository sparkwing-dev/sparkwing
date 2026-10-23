package capability

import (
	"context"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
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
var dataPolicies = []DataPolicy{
	{DataDownload, DataRead, true, false, false},
	{DataUpload, DataWrite, false, true, false},
	{DataCommit, DataWrite, false, true, false},
	{DataHasMany, DataRead, true, false, false},
	{DataRecord, DataWrite, false, false, true},
	{DataLookup, DataRead, true, false, false},
}

// DataTarget carries selectors for C1's read-set and receipt checks, not asserted provenance.
type DataTarget struct {
	Digests     []string
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
	if edges == nil {
		return policyFailure()
	}
	var policy *DataPolicy
	for i := range dataPolicies {
		if dataPolicies[i].Operation == target.Operation {
			policy = &dataPolicies[i]
			break
		}
	}
	if policy == nil {
		return &apierr.Error{Code: apierr.BadRequest, Message: "unknown data operation"}
	}
	if target.RecordKind == "log" {
		return &apierr.Error{Code: apierr.BadRequest, Message: "log reads use the controller log routes"}
	}
	c := authorized.Principal.Capability
	grant := policy.Grant
	sourceBundle := authorized.Access.Binding == BindSourceBundle
	if sourceBundle {
		if target.Operation != DataDownload || !matchesCredential(authorized.Principal, CredentialBootstrap) || c.Delivery != DeliveryBootstrap {
			return forbidden(apierr.ReasonBinding)
		}
		grant = SourceRead
	}
	if c.Audience != Controller {
		return forbidden(apierr.ReasonAudience)
	}
	if !slices.Contains(c.Grants, grant) || !slices.Contains(authorized.Access.Grants, grant) {
		return forbidden(apierr.ReasonMissingGrant)
	}
	if c.Team == "" || authorized.Target.Team != c.Team {
		return forbidden(apierr.ReasonTeam)
	}
	scope := c.Data
	if scope == nil || scope.RepositoryKey == "" {
		return forbidden(apierr.ReasonRepositoryUnauthorized)
	}
	if authorized.ResultReplay || authorized.LogTailAttempt != 0 || authorized.Access.Binding != BindData && !sourceBundle {
		return forbidden(apierr.ReasonBinding)
	}
	if authorized.Access.Liveness != LiveSensitive && authorized.Access.Liveness != LiveExpansion &&
		!(authorized.Access.Liveness == LiveCredential && (scope.Use == DataRemoteCache || scope.Use == DataPublish)) {
		return forbidden(apierr.ReasonBinding)
	}
	if sourceBundle {
		if authorized.Access.Liveness != LiveSensitive || scope.Use != DataClaim && !(scope.Use == DataExpansion && c.Unit == UnitEval) || scope.RunID == "" || scope.RunID != c.Binding.RunID || target.RunID != scope.RunID || target.NodeID != scope.NodeID || target.Generation != scope.Generation {
			return forbidden(apierr.ReasonBinding)
		}
		return edges.CheckDataAccess(ctx, authorized, target)
	}
	if err := dataUse(authorized.Principal, *scope, policy.Grant); err != nil {
		return err
	}
	if policy.Grant == DataWrite && !dataWriter(*scope, target) {
		return forbidden(apierr.ReasonBinding)
	}
	return edges.CheckDataAccess(ctx, authorized, target)
}

func dataUse(p Principal, scope DataScope, grant Grant) error {
	switch scope.Use {
	case DataRemoteCache:
		if grant != DataRead {
			return forbidden(apierr.ReasonReadOnly)
		}
		if !scope.VerifiedDefaultOnly || !matchesCredential(p, CredentialRepoReader) {
			return forbidden(apierr.ReasonBinding)
		}
	case DataPublish:
		if !matchesCredential(p, CredentialPublisher) {
			return forbidden(apierr.ReasonBinding)
		}
	case DataClaim:
		if !isClaim(p) || p.Capability.Delivery == DeliveryBootstrap || scope.RunID != p.Capability.Binding.RunID {
			return forbidden(apierr.ReasonBinding)
		}
		if b := p.Capability.Binding; b.Kind == BindingNode && (scope.NodeID != b.NodeID || scope.Generation != b.Generation || scope.Attempt != b.Attempt) {
			return forbidden(apierr.ReasonBinding)
		}
	case DataExpansion:
		if grant != DataRead {
			return forbidden(apierr.ReasonReadOnly)
		}
		if !isClaim(p) || p.Capability.Binding.Expansion == nil || scope.RunID != p.Capability.Binding.RunID ||
			scope.NodeID != p.Capability.Binding.NodeID || scope.Generation != p.Capability.Binding.Generation {
			return forbidden(apierr.ReasonBinding)
		}
	default:
		return forbidden(apierr.ReasonBinding)
	}
	return nil
}

func dataWriter(scope DataScope, target DataTarget) bool {
	return scope.RunID != "" && target.RunID == scope.RunID &&
		(scope.NodeID == "" || target.NodeID == scope.NodeID) &&
		(scope.Attempt == 0 || target.Attempt == scope.Attempt) &&
		(scope.Generation == 0 || target.Generation == scope.Generation)
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
// Data GETs expire after 60 seconds; log PUTs after 15 minutes, as owned by pkg/limits.
type DataSigner interface {
	Sign(context.Context, ObjectToSign) (SignedObject, error)
}
