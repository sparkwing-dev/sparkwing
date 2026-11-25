package store

import (
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// safety: a session resolves differently for each child, which fixes its team and its lifetime, so both join the key.
type authorityMemoKey struct {
	ref         capability.AuthorityRef
	childTeam   string
	memberToken bool
}

func newAuthorityMemoKey(ref capability.AuthorityRef, sessionChild []authorityNode) authorityMemoKey {
	key := authorityMemoKey{ref: ref}
	if len(sessionChild) != 0 {
		key.childTeam = sessionChild[0].principal.Capability.Team
		key.memberToken = sessionChild[0].memberToken
	}
	return key
}

// safety: liveness and inherited grants depend on time and on the whole chain, so only reads are kept and those
// checks run again on every use.
type verifiedAuthority struct {
	raw, verified authorityNode
}

func (t *storeTx) rememberAuthority(key authorityMemoKey, entry verifiedAuthority) {
	if t.verifiedAuthority == nil {
		t.verifiedAuthority = map[authorityMemoKey]verifiedAuthority{}
	}
	t.verifiedAuthority[key] = verifiedAuthority{raw: cloneAuthorityNode(entry.raw), verified: cloneAuthorityNode(entry.verified)}
}

// safety: any write, savepoint or savepoint rollback by this transaction may change ancestry rows or
// release row locks taken after a savepoint, so every remembered node is read again afterwards.
func (t *storeTx) forgetAuthority() {
	t.verifiedAuthority = nil
}

func (t *storeTx) forgetAuthorityOnWrite(q string) {
	if strings.Contains(q, "RETURNING") || strings.Contains(q, "returning") {
		t.forgetAuthority()
	}
}

// safety: callers own the nodes they receive, so no edit can reach a remembered verification.
func cloneAuthorityNode(node authorityNode) authorityNode {
	node.principal.Capability = cloneCapability(node.principal.Capability)
	node.requirement = clonePointer(node.requirement)
	return node
}

func cloneCapability(c capability.Capability) capability.Capability {
	c.Host = clonePointer(c.Host)
	c.Grants = slices.Clone(c.Grants)
	c.Binding.ClaimBy = clonePointer(c.Binding.ClaimBy)
	c.Binding.Deadline = clonePointer(c.Binding.Deadline)
	c.Binding.Push = clonePointer(c.Binding.Push)
	c.Binding.Expansion = clonePointer(c.Binding.Expansion)
	c.Parent = clonePointer(c.Parent)
	c.ReservedPools = slices.Clone(c.ReservedPools)
	c.Repositories = slices.Clone(c.Repositories)
	c.Children = slices.Clone(c.Children)
	if c.Data != nil {
		data := *c.Data
		data.ReadRefs = slices.Clone(data.ReadRefs)
		c.Data = &data
	}
	c.Billing = clonePointer(c.Billing)
	c.ExpiresAt = clonePointer(c.ExpiresAt)
	c.RevokedAt = clonePointer(c.RevokedAt)
	return c
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
