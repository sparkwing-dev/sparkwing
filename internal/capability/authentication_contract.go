package capability

import (
	"context"
	"time"
)

// RequestEvidence carries decoded selectors and the digest of the exact result body.
// The HTTP boundary supplies it before authentication; it grants no authority.
// Authenticate overwrites Method and Pattern with the matched route.
type RequestEvidence struct {
	Method       string
	Pattern      string
	Target       Target
	ResultDigest string
}

// CredentialLookup contains no reusable secret. Digest hashes the complete presentation.
// Peer is accepted only from a transport that verified the Unix uid or Windows SID.
type CredentialLookup struct {
	Class    CredentialClass
	Digest   [32]byte
	Audience Audience
	Peer     Subject
}

// AuthorityState is one persisted authority and its current membership verdict.
// MembershipValid includes the authority's own team and accountable subject,
// including the awarded team's membership when its parent is a cross-team launcher.
type AuthorityState struct {
	Principal       Principal
	MembershipValid bool
}

// RecordedResult identifies the committed write, including its actor's fence.
// A digest alone cannot authorize a replay against another resource or operation.
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
// Eval requires a live controller ownership lease as well as its own lease.
type ExpansionState struct {
	Lease                    LeaseRef
	ExpiresAt                time.Time
	ControllerLeaseExpiresAt *time.Time
}

// BindingState is a committed snapshot, not a caller assertion or cached liveness verdict.
// FinishedAt is the final attempt's persisted finish; Current retains its last fence.
// HereClaimed survives owner loss so reclaim cannot restart the first-claim window.
type BindingState struct {
	Current        Binding
	LeaseExpiresAt time.Time
	FinishedAt     *time.Time
	RunCancelled   bool
	RunFinished    bool
	HereClaimed    bool
	LogSealed      bool
	ProcessAlive   bool
	Result         *RecordedResult
	Expansion      *ExpansionState
}

// AuthenticationSnapshot uses one store clock and a complete leaf-to-root ancestry.
// A local peer or configured anonymous principal has one authority and no parent.
type AuthenticationSnapshot struct {
	Now         time.Time
	Authorities []AuthorityState
	Binding     BindingState
}

// AuthenticationReader reads authority, membership and binding facts in one consistent
// snapshot. Unknown presentations return unauthenticated; failed reads return errors.
// It owns persistence, while the verifier owns the route's liveness decision.
type AuthenticationReader interface {
	ReadAuthentication(context.Context, CredentialLookup, RequestEvidence) (AuthenticationSnapshot, error)
}

// NewAuthenticator verifies fresh snapshots so ancestry and membership changes
// take effect without a credential-cache invalidation window.
func NewAuthenticator(reader AuthenticationReader) Authenticator {
	panic("unimplemented: c2-credentials")
}

// ValidateAuthority checks complete ancestry and current membership for both
// request authentication and transactional authority resolution. Unit liveness
// remains separate because result replay has a narrower terminal exception.
func ValidateAuthority(chain []AuthorityState, now time.Time) (Principal, error) {
	panic("unimplemented: c2-credentials")
}
