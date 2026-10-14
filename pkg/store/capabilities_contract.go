package store

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// Issue is the only persistent capability constructor. It participates in the locked claim,
// intake or exchange transaction and uses the store clock for ancestry, expiry and fences.
// The caller must roll back the transaction on error and expose Raw only after commit.
// Tagged parent_kind/parent_id resolve through C2 against the capability or session
// store. Here descendants retain the requesting session/token ancestry. The full
// CapabilityRecord envelope and every indexed projection persist in the same write.
func (s *Store) Issue(ctx context.Context, tx *storeTx, request capability.IssueRequest) (capability.Issued, error) {
	panic("unimplemented: c2-credentials")
}

// CapabilityRecord persists the complete authority shape with an explicit version.
// Value.Digest is excluded from JSON and lives in the digest column. Raw bearers
// never persist. Indexed columns are exact projections of this envelope, validated
// on load and updated with it in one transaction; mutable state cannot fill gaps.
type CapabilityRecord struct {
	Version int                   `json:"version"`
	Value   capability.Capability `json:"value"`
}

// Validate checks the record version and complete C2 authority shape before persistence.
func (record CapabilityRecord) Validate() error {
	panic("unimplemented: c1-authority")
}

// ResolveAuthority rechecks tagged session/capability authority inside the current write transaction.
// Sessions resolve current membership and grants; capability prefixes remain separate
// claim fences. A mismatch between Actor.Authority and its fence is refused.
func (tx *storeTx) ResolveAuthority(ctx context.Context, ref capability.AuthorityRef) (capability.Principal, error) {
	panic("unimplemented: c1-authority")
}

// AuthGenerations reads the credential and every ancestor plus team membership in
// one consistent snapshot. Missing/revoked ancestry fails closed. Cross-team launcher
// roots remain in the vector. Team must equal the leaf authority's team and may
// be empty only for unbound deployment authority.
func (s *Store) AuthGenerations(ctx context.Context, authority capability.AuthorityRef, team string) (capability.AuthGenerations, error) {
	panic("unimplemented: c1-authority")
}

// RevokeAuthority resolves the actor and target transactionally, then revokes the
// tenant capability/session and advances its generation in that same transaction.
// Descendant cache reuse detects the changed ancestor without per-child writes.
func (t *Tenant) RevokeAuthority(ctx context.Context, actor Actor, target capability.AuthorityRef) error {
	panic("unimplemented: c1-authority")
}

// RevokeAuthority applies deployment authority to service/operator roots only.
// It advances the root generation used by descendants across all awarded teams.
func (o *Operator) RevokeAuthority(ctx context.Context, actor Actor, target capability.AuthorityRef) error {
	panic("unimplemented: c1-authority")
}

// MembershipUpdate sets a role, or removes the member when Role is nil.
type MembershipUpdate struct {
	AccountID string
	Role      *Role
}

// UpdateMembership authorizes the tagged actor, applies the membership change and
// increments the team's auth generation atomically. It preserves last-owner rules.
func (t *Tenant) UpdateMembership(ctx context.Context, actor Actor, change MembershipUpdate) error {
	panic("unimplemented: c1-authority")
}

// safety: authority mutation and cache invalidation must share one commit.
// Revocation increments each named capability/session generation; membership
// changes increment the named team's auth_generation. No standalone write is valid.
func advanceAuthGenerationsTx(ctx context.Context, tx *storeTx, authorities []capability.AuthorityRef, membershipTeam *Team) error {
	panic("unimplemented: c1-authority")
}

var _ capability.AuthorityResolver = (*storeTx)(nil)

// safety: SQL indexes accelerate lookup but cannot broaden the persisted authority.
// The loader validates CapabilityRecord, compares every indexed projection against
// its envelope, restores Digest from the separate digest column and fails on drift.
func loadCapabilityTx(ctx context.Context, tx *storeTx, prefix string) (capability.Capability, error) {
	panic("unimplemented: c1-authority")
}
