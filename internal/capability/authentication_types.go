package capability

import (
	"context"
	"time"
)

// RequestEvidence carries decoded selectors and the digest of the exact result body.
type RequestEvidence struct {
	Method       string
	Pattern      string
	Target       Target
	ResultDigest string
}

// CredentialLookup contains no reusable secret. Digest hashes the complete presentation.
type CredentialLookup struct {
	Class    CredentialClass
	Digest   [32]byte
	Audience Audience
	Peer     Subject
}

// AuthorityState is one persisted authority and its current membership verdict.
type AuthorityState struct {
	Principal       Principal
	MembershipValid bool
}

// RecordedResult identifies the committed write, including its actor's fence.
type RecordedResult struct {
	Actor      AuthorityRef
	Method     string
	Pattern    string
	RunID      string
	NodeID     string
	ClaimID    string
	Generation int64
	Attempt    int64
	Digest     string
}

// ExpansionState supplies the owner/eval lease independently of the terminal source.
type ExpansionState struct {
	Lease                    LeaseRef
	ExpiresAt                time.Time
	ControllerLeaseExpiresAt *time.Time
}

// BindingState is a committed snapshot, not a caller assertion or cached liveness verdict.
type BindingState struct {
	Current        Binding
	LeaseExpiresAt time.Time
	FinishedAt     *time.Time
	RunCancelled   bool
	CancelReason   string
	RunFinished    bool
	HereClaimed    bool
	LogSealed      bool
	ProcessAlive   bool
	Result         *RecordedResult
	Expansion      *ExpansionState
}

// AuthenticationSnapshot uses one store clock and a complete leaf-to-root ancestry.
type AuthenticationSnapshot struct {
	Now         time.Time
	Authorities []AuthorityState
	Binding     BindingState
}

// AuthenticationReader reads authority, membership and binding facts in one store snapshot.
type AuthenticationReader interface {
	ReadAuthentication(context.Context, CredentialLookup, RequestEvidence) (AuthenticationSnapshot, error)
}
