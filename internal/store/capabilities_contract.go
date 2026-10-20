package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

// CapabilityRecord preserves the complete authority independently of its lookup indexes.
type CapabilityRecord struct {
	Version int                   `json:"version"`
	Value   capability.Capability `json:"value"`
	Origin  *CapabilityOrigin     `json:"origin,omitempty"`
}

// CapabilityOrigin retains the trusted provisioning kind and registration.
type CapabilityOrigin struct {
	Personal       bool                     `json:"personal,omitempty"`
	Requirement    *authorityRequirement    `json:"requirement,omitempty"`
	Kind           capability.ProvisionKind `json:"kind"`
	RegistrationID string                   `json:"registration_id,omitempty"`
}

// Validate rejects incomplete or unknown persisted authority shapes.
func (r CapabilityRecord) Validate() error {
	c := r.Value
	if r.Origin != nil && !validCapabilityOrigin(c, *r.Origin) {
		return authorityError(apierr.IntegrityFailure, "invalid capability origin")
	}
	if r.Version != 1 || c.Prefix == "" || c.Subject.ID == "" || c.IssuedAt.IsZero() {
		return authorityError(apierr.IntegrityFailure, "invalid capability envelope")
	}
	if !slices.Contains([]capability.SubjectKind{capability.SubjectAccount, capability.SubjectMachine, capability.SubjectService, capability.SubjectOperator, capability.SubjectLocal, capability.SubjectAnonymous}, c.Subject.Kind) {
		return authorityError(apierr.IntegrityFailure, "invalid capability subject")
	}
	if c.Audience != capability.Controller && c.Audience != capability.NodeHost {
		return authorityError(apierr.IntegrityFailure, "invalid capability audience")
	}
	seen := map[capability.Grant]bool{}
	for _, g := range c.Grants {
		if !slices.Contains(capability.Grants(), g) || seen[g] {
			return authorityError(apierr.IntegrityFailure, "invalid capability grants")
		}
		seen[g] = true
	}
	if c.Parent != nil && !validAuthorityRef(*c.Parent) {
		return authorityError(apierr.IntegrityFailure, "invalid capability parent")
	}
	if c.ExpiresAt != nil && !c.ExpiresAt.After(c.IssuedAt) {
		return authorityError(apierr.IntegrityFailure, "invalid capability expiry")
	}
	if c.Team == "" && (c.Binding.Kind != capability.BindingNone || (c.Subject.Kind != capability.SubjectService && c.Subject.Kind != capability.SubjectOperator)) {
		return authorityError(apierr.IntegrityFailure, "unbound capability has tenant authority")
	}
	switch c.Binding.Kind {
	case capability.BindingNone:
		if c.Binding.RunID != "" || c.Binding.NodeID != "" || c.Binding.Generation != 0 || c.Binding.Holder != "" || c.Binding.Push != nil || c.Binding.Expansion != nil {
			return authorityError(apierr.IntegrityFailure, "unbound capability has a resource fence")
		}
	case capability.BindingPush:
		if c.Binding.Push == nil || c.Binding.Push.RepositoryKey == "" || c.Binding.Push.Ref == "" || c.Binding.Push.SHA == "" || c.Binding.Push.WorkflowRunID == "" {
			return authorityError(apierr.IntegrityFailure, "invalid push binding")
		}
	case capability.BindingNode, capability.BindingRun, capability.BindingTrigger:
		if c.Team == "" || c.Binding.RunID == "" || (c.Binding.Kind == capability.BindingNode && c.Binding.NodeID == "") {
			return authorityError(apierr.IntegrityFailure, "invalid resource binding")
		}
	default:
		return authorityError(apierr.IntegrityFailure, "unknown capability binding")
	}
	if c.Binding.Generation < 0 || c.Binding.Attempt < 0 {
		return authorityError(apierr.IntegrityFailure, "invalid capability fence")
	}
	if c.Issuance != capability.Delegate && c.Issuance != capability.Award && c.Issuance != capability.Exchange {
		return authorityError(apierr.IntegrityFailure, "unknown capability issuance")
	}
	if c.Unit == "" {
		if c.UnitID != "" || c.Purpose != "" || c.Delivery != "" || c.Billing != nil || c.Issuance != capability.Delegate {
			return authorityError(apierr.IntegrityFailure, "incomplete award identity")
		}
	} else {
		p, ok := awardPolicy(c.Purpose)
		if !ok || p.Unit != c.Unit || c.UnitID == "" || c.Parent == nil || c.Team == "" || c.Billing == nil {
			return authorityError(apierr.IntegrityFailure, "invalid award identity")
		}
		if c.Delivery != capability.DeliveryDirect && c.Delivery != capability.DeliveryBootstrap {
			return authorityError(apierr.IntegrityFailure, "invalid award delivery")
		}
		if c.Delivery == capability.DeliveryBootstrap && !slices.Contains(capability.BootstrapUnits(), c.Unit) {
			return authorityError(apierr.IntegrityFailure, "invalid bootstrap unit")
		}
		b := c.Billing
		expected := capability.BillingUnit(c.Unit)
		if c.Unit == capability.UnitTrigger {
			expected = capability.BillingHost
		}
		if c.Unit == capability.UnitNode {
			expected = capability.BillingJob
		}
		if b.PayerTeam != c.Team || b.ReservationID == "" || b.ExecutorClass == "" || b.Unit != expected || (b.Start != capability.BillingAtStart && b.Start != capability.BillingAtAward) {
			return authorityError(apierr.IntegrityFailure, "invalid billing provenance")
		}
	}
	return nil
}

func authorityError(code apierr.Code, message string) error {
	if code == apierr.Forbidden {
		return apierr.Authorization(apierr.ReasonMissingGrant, message)
	}
	return &apierr.Error{Code: code, Message: message}
}

func validAuthorityRef(ref capability.AuthorityRef) bool {
	return ref.ID != "" && (ref.Kind == capability.AuthorityCapability || ref.Kind == capability.AuthoritySession)
}

func awardPolicy(purpose capability.AwardPurpose) (capability.AwardPolicy, bool) {
	for _, p := range capability.AwardPolicies() {
		if p.Purpose == purpose {
			return p, true
		}
	}
	return capability.AwardPolicy{}, false
}

func optionalString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func optionalTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UnixNano()
}

var authorityColumns = []string{"prefix", "team", "subject_kind", "subject_id", "audience", "grants", "issuance", "authority_version", "parent_kind", "parent_id", "award_purpose", "delivery_purpose", "unit_id", "billing_unit", "bind_kind", "bind_run", "bind_node", "bind_generation", "bind_holder", "claim_by", "reserved_pools", "created_at", "expires_at", "revoked_at", "metered", "payer_team", "executor_class", "reservation_id", "billing_start", "unit_kind", "eval_unit_generation", "repo_key", "repo_scope", "ref_scope", "push_sha"}

func authorityProjection(c capability.Capability) ([]any, error) {
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return nil, err
	}
	pools, err := json.Marshal(c.ReservedPools)
	if err != nil {
		return nil, err
	}
	repos, err := json.Marshal(c.Repositories)
	if err != nil {
		return nil, err
	}
	var parentKind, parentID, billingUnit, payer, executor, reservation, start, expansion any
	metered := int64(0)
	if c.RootMetered {
		metered = 1
	}
	if c.Parent != nil {
		parentKind = string(c.Parent.Kind)
		parentID = c.Parent.ID
	}
	if c.Billing != nil {
		b := c.Billing
		billingUnit = string(b.Unit)
		payer = b.PayerTeam
		executor = b.ExecutorClass
		reservation = b.ReservationID
		start = string(b.Start)
		metered = 0
		if b.Metered {
			metered = 1
		}
	}
	if c.Binding.Expansion != nil {
		expansion = c.Binding.Expansion.Generation
	}
	repo, ref, sha := "", "", ""
	if c.Data != nil {
		repo = c.Data.RepositoryKey
		ref = c.Data.WriteRef
	}
	if c.Binding.Push != nil {
		repo = c.Binding.Push.RepositoryKey
		ref = c.Binding.Push.Ref
		sha = c.Binding.Push.SHA
	}
	var generation any
	if c.Binding.Generation != 0 {
		generation = c.Binding.Generation
	}
	return []any{c.Prefix, optionalString(c.Team), string(c.Subject.Kind), c.Subject.ID, string(c.Audience), grants, string(c.Issuance), int64(1), parentKind, parentID, optionalString(string(c.Purpose)), optionalString(string(c.Delivery)), optionalString(c.UnitID), billingUnit, string(c.Binding.Kind), optionalString(c.Binding.RunID), optionalString(c.Binding.NodeID), generation, optionalString(c.Binding.Holder), optionalTime(c.Binding.ClaimBy), pools, c.IssuedAt.UnixNano(), optionalTime(c.ExpiresAt), optionalTime(c.RevokedAt), metered, payer, executor, reservation, start, optionalString(string(c.Unit)), expansion, repo, repos, ref, sha}, nil
}

func persistCapabilityTx(ctx context.Context, tx *storeTx, c capability.Capability, origin ...*CapabilityOrigin) error {
	record := CapabilityRecord{Version: 1, Value: c}
	if len(origin) > 0 {
		record.Origin = origin[0]
	}
	if err := record.Validate(); err != nil {
		return err
	}
	if c.Digest == ([32]byte{}) {
		return authorityError(apierr.IntegrityFailure, "missing bearer digest")
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	values, err := authorityProjection(c)
	if err != nil {
		return err
	}
	values = append(values, hex.EncodeToString(c.Digest[:]), body, c.Subject.ID)
	columns := append(slices.Clone(authorityColumns), "digest", "authority_json", "created_by")
	_, err = tx.ExecContext(ctx, "INSERT INTO capabilities ("+strings.Join(columns, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")+")", values...)
	return err
}

// safety: indexed columns may accelerate lookup but cannot add authority to the envelope.
func loadCapabilityTx(ctx context.Context, tx *storeTx, prefix string) (capability.Capability, error) {
	record, err := loadCapabilityRecordTx(ctx, tx, prefix)
	return record.Value, err
}

func loadCapabilityRecordTx(ctx context.Context, tx *storeTx, prefix string) (CapabilityRecord, error) {
	var digest string
	var body []byte
	actual := make([]any, len(authorityColumns))
	dest := []any{&digest, &body}
	for i := range actual {
		dest = append(dest, &actual[i])
	}
	err := tx.QueryRowContext(ctx, "SELECT digest,authority_json,"+strings.Join(authorityColumns, ",")+" FROM capabilities WHERE prefix = ?", prefix).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return CapabilityRecord{}, authorityError(apierr.Unauthenticated, "authority is unavailable")
	}
	if err != nil {
		return CapabilityRecord{}, err
	}
	var record CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		return CapabilityRecord{}, authorityError(apierr.IntegrityFailure, "invalid capability envelope")
	}
	if err := record.Validate(); err != nil {
		return CapabilityRecord{}, err
	}
	expected, err := authorityProjection(record.Value)
	if err != nil {
		return CapabilityRecord{}, err
	}
	for i, v := range actual {
		if !equalProjection(v, expected[i]) {
			return CapabilityRecord{}, authorityError(apierr.IntegrityFailure, "capability projection mismatch: "+authorityColumns[i])
		}
	}
	bytes, err := hex.DecodeString(digest)
	if err != nil || len(bytes) != sha256.Size {
		return CapabilityRecord{}, authorityError(apierr.IntegrityFailure, "invalid bearer digest")
	}
	copy(record.Value.Digest[:], bytes)
	return record, nil
}

func equalProjection(a, b any) bool {
	if v, ok := a.([]byte); ok {
		a = string(v)
	}
	if v, ok := b.([]byte); ok {
		b = string(v)
	}
	return reflect.DeepEqual(a, b)
}

type authorityNode struct {
	principal   capability.Principal
	requirement *authorityRequirement
}

func readAuthorityNodeTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) (authorityNode, error) {
	if !validAuthorityRef(ref) {
		return authorityNode{}, authorityError(apierr.Unauthenticated, "invalid authority reference")
	}
	node := authorityNode{principal: capability.Principal{Authority: ref}}
	if ref.Kind == capability.AuthorityCapability {

		record, err := loadCapabilityRecordTx(ctx, tx, ref.ID)
		if err != nil {
			return node, err
		}
		node.principal.Capability = record.Value
		if record.Origin != nil {
			node.requirement = record.Origin.Requirement
		}
		node.principal.Class = capability.CredentialBearer
	} else {
		var account, parent string
		var created, expires, epoch int64
		var revoked sql.NullInt64
		c := capability.Capability{Audience: capability.Controller, Binding: capability.Binding{Kind: capability.BindingNone}}
		err := tx.QueryRowContext(ctx, "SELECT principal,COALESCE(team,''),account_id,capability_prefix,created_at,expires_at,revoked_at,membership_epoch FROM sessions WHERE hash = ?", ref.ID).Scan(&c.Subject.ID, &c.Team, &account, &parent, &created, &expires, &revoked, &epoch)
		if err != nil {
			return node, err
		}
		if account == "" {
			return node, authorityError(apierr.Unauthenticated, "session identity is unavailable")
		}
		c.Subject = capability.Subject{Kind: capability.SubjectAccount, ID: account}
		c.IssuedAt = time.Unix(0, created).UTC()
		expiry := time.Unix(0, expires).UTC()
		c.ExpiresAt = &expiry
		if revoked.Valid {
			at := time.Unix(0, revoked.Int64).UTC()
			c.RevokedAt = &at
		}
		if parent != "" {
			c.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: parent}
			c.Issuance = capability.Delegate
		}

		node.principal.Capability = c
		node.principal.Class = capability.CredentialSession
		if c.Team != "" {
			node.requirement = &authorityRequirement{Account: account, Epoch: epoch}
		}
	}
	return node, nil
}

func readAuthorityTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef, lock bool) (authorityNode, error) {
	if lock {
		if err := lockAuthorityTx(ctx, tx, []capability.AuthorityRef{ref}); err != nil {
			return authorityNode{}, err
		}
	}
	node, err := readAuthorityNodeTx(ctx, tx, ref)
	if err != nil {
		return node, err
	}

	c := node.principal.Capability
	if !capability.CredentialAlive(c, tx.now()) {
		return node, authorityError(apierr.Unauthenticated, "authority has ended")
	}
	if node.requirement != nil {
		if err := checkAuthorityRequirementTx(ctx, tx, c.Team, *node.requirement); err != nil {
			return node, err
		}
	}
	if c.Subject.Kind == capability.SubjectAccount {
		if !node.readsMembership() {
			var account string
			err := tx.QueryRowContext(ctx, "SELECT id FROM accounts WHERE id=?", c.Subject.ID).Scan(&account)
			return node, err
		}
		var role Role
		if err := tx.QueryRowContext(ctx, "SELECT role FROM memberships WHERE team = ? AND account_id = ?", c.Team, c.Subject.ID).Scan(&role); err != nil {
			return node, err
		}
		if !role.Valid() {
			return node, authorityError(apierr.Unauthenticated, "membership is unavailable")
		}
		if c.Unit == "" {
			allowed := ScopesForRole(role)
			if c.Data != nil {
				if c.Data.Use == capability.DataRemoteCache && slices.Contains(allowed, ScopeRunsWrite) {
					allowed = append(allowed, string(capability.DataRead))
				}
				if c.Data.Use == capability.DataPublish && slices.Contains(allowed, ScopeRunsWrite) {
					allowed = append(allowed, string(capability.DataWrite))
				}
			}
			if node.principal.Class == capability.CredentialSession {
				for _, scope := range allowed {
					node.principal.Capability.Grants = append(node.principal.Capability.Grants, capability.Grant(scope))
				}
			}
			node.principal.Capability.Grants = slices.DeleteFunc(slices.Clone(node.principal.Capability.Grants), func(g capability.Grant) bool { return !slices.Contains(allowed, string(g)) })
		}
	}
	return node, nil
}

// readsMembership reports whether an account node's authority depends on
// its team membership rather than on the account alone.
func (node authorityNode) readsMembership() bool {
	c := node.principal.Capability
	return c.Subject.Kind == capability.SubjectAccount && !(c.Team == "" && node.principal.Class == capability.CredentialSession && c.Parent == nil)
}

func authorityChainTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef, lock bool, writes ...string) ([]authorityNode, error) {
	if lock {
		if err := lockAuthorityTx(ctx, tx, []capability.AuthorityRef{ref}, writes...); err != nil {
			return nil, err
		}
	}
	seen := map[capability.AuthorityRef]bool{}
	var chain []authorityNode
	for {
		if seen[ref] {
			return nil, authorityError(apierr.IntegrityFailure, "cyclic authority ancestry")
		}
		seen[ref] = true
		node, err := readAuthorityTx(ctx, tx, ref, false)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, authorityError(apierr.Unauthenticated, "authority ancestry is unavailable")
		}
		if err != nil {
			return nil, err
		}
		chain = append(chain, node)
		parent := node.principal.Capability.Parent
		if parent == nil {
			break
		}
		ref = *parent
	}
	now := tx.now()
	for _, node := range chain {
		c := node.principal.Capability
		if !capability.CredentialAlive(c, now) {
			return nil, authorityError(apierr.Unauthenticated, "authority expired while ancestry was locked")
		}
	}
	for i := len(chain) - 2; i >= 0; i-- {
		child := &chain[i].principal.Capability
		parent := chain[i+1].principal.Capability
		if chain[i].principal.Class == capability.CredentialSession && (child.Team != parent.Team || child.Subject != parent.Subject || parent.Audience != capability.Controller || parent.Binding.Kind != capability.BindingNone || parent.Unit != "") {
			return nil, authorityError(apierr.Unauthenticated, "session parent identity differs")
		}
		if child.Issuance == capability.Delegate {
			child.Grants = capability.InheritedGrants(chain[i+1].principal, *child)
		}
		if child.Purpose == capability.PurposeHere && !slices.Contains(parent.Grants, capability.RunsWrite) {
			return nil, authorityError(apierr.Unauthenticated, "here requester lost run authority")
		}
	}
	return chain, nil
}

func discoverAuthorityChainTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) ([]authorityNode, error) {
	seen := map[capability.AuthorityRef]bool{}
	var chain []authorityNode
	for {
		if seen[ref] {
			return nil, authorityError(apierr.IntegrityFailure, "cyclic authority ancestry")
		}
		seen[ref] = true
		node, err := readAuthorityNodeTx(ctx, tx, ref)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, authorityError(apierr.Unauthenticated, "authority ancestry is unavailable")
		}
		if err != nil {
			return nil, err
		}
		chain = append(chain, node)
		if node.principal.Capability.Parent == nil {
			return chain, nil
		}
		ref = *node.principal.Capability.Parent
	}
}

var errAuthorityChanged = errors.New("authority ancestry changed during lock discovery")

func authorityMembershipKeys(chain []authorityNode) []storeLockKey {
	var keys []storeLockKey
	for _, node := range chain {
		c := node.principal.Capability
		if node.readsMembership() {
			keys = append(keys, storeLockKey{tier: lockMembership, keys: [3]string{c.Team, c.Subject.ID}})
		}
		if node.requirement != nil {
			keys = append(keys, storeLockKey{tier: lockMembership, keys: [3]string{c.Team, node.requirement.Account}})
			if node.requirement.MembersCanAddMachines {
				keys = append(keys, storeLockKey{tier: lockTeam, keys: [3]string{c.Team}})
			}
		}
	}
	slices.SortFunc(keys, compareStoreLocks)
	return slices.Compact(keys)
}

// The unlocked walk discovers the entire set before any lock is acquired. A
// concurrent session team switch restarts the transaction rather than extending
// the set while holding authority locks.
func lockAuthorityTx(ctx context.Context, tx *storeTx, leaves []capability.AuthorityRef, writes ...string) error {
	var set []authorityLock
	var memberships []storeLockKey
	chains := make(map[capability.AuthorityRef][]authorityNode)
	for _, leaf := range leaves {
		chain, err := discoverAuthorityChainTx(ctx, tx, leaf)
		if err != nil {
			return err
		}
		chains[leaf] = chain
		memberships = append(memberships, authorityMembershipKeys(chain)...)
		for i, node := range chain {
			ref := node.principal.Authority
			write := slices.Contains(writes, ref.ID) || ref.Kind == capability.AuthorityCapability && registeredClaimant(node.principal.Capability)
			if at := slices.IndexFunc(set, func(h authorityLock) bool { return h.ref == ref }); at >= 0 {
				set[at].write = set[at].write || write
				continue
			}
			set = append(set, authorityLock{ref, len(chain) - 1 - i, write})
		}
	}
	slices.SortFunc(memberships, compareStoreLocks)
	for _, key := range slices.Compact(memberships) {
		query, args := "SELECT account_id FROM memberships WHERE team=? AND account_id=?", []any{key.keys[0], key.keys[1]}
		if key.tier == lockTeam {
			query, args = "SELECT name FROM teams WHERE name=?", []any{key.keys[0]}
		}
		var id string
		if err := tx.QueryRowContext(ctx, query+tx.forShare(), args...).Scan(&id); errors.Is(err, sql.ErrNoRows) {
			return authorityError(apierr.Unauthenticated, "membership is unavailable")
		} else if err != nil {
			return err
		}
	}
	slices.SortFunc(set, compareAuthorityLocks)
	for _, h := range set {
		query := "SELECT prefix FROM capabilities WHERE prefix=?"
		if h.ref.Kind == capability.AuthoritySession {
			query = "SELECT hash FROM sessions WHERE hash=?"
		}
		mode := tx.forShare()
		if h.write {
			mode = tx.forNoKeyUpdate()
		}
		var key string
		if err := tx.QueryRowContext(ctx, query+mode, h.ref.ID).Scan(&key); errors.Is(err, sql.ErrNoRows) {
			return errAuthorityChanged
		} else if err != nil {
			return err
		}
	}
	for leaf, discovered := range chains {
		current, err := discoverAuthorityChainTx(ctx, tx, leaf)
		if err != nil {
			return err
		}
		if len(current) != len(discovered) || !slices.Equal(authorityMembershipKeys(current), authorityMembershipKeys(discovered)) {
			return errAuthorityChanged
		}
		for i := range current {
			if current[i].principal.Authority != discovered[i].principal.Authority {
				return errAuthorityChanged
			}
		}
	}
	return nil
}

// ResolveAuthority reads live ancestry and locks it in write transactions.
func (tx *storeTx) ResolveAuthority(ctx context.Context, ref capability.AuthorityRef) (capability.Principal, error) {
	if tx.scheduleAdmission != nil && ref == tx.scheduleAdmission.authority() {
		return tx.scheduleAdmission.resolve(ctx, tx)
	}
	if tx.appAdmission != nil && ref == tx.appAdmission.authority() {
		return tx.appAdmission.resolve(ctx, tx)
	}
	chain, err := authorityChainTx(ctx, tx, ref, true)
	if err != nil {
		return capability.Principal{}, err
	}
	return chain[0].principal, nil
}

func resolveActorTx(ctx context.Context, tx *storeTx, actor Actor) (capability.Principal, error) {
	if (actor.Authority.Kind == capability.AuthoritySession && actor.CapabilityPrefix != "") || (actor.Authority.Kind == capability.AuthorityCapability && actor.CapabilityPrefix != "" && actor.CapabilityPrefix != actor.Authority.ID) {
		return capability.Principal{}, authorityError(apierr.Forbidden, "actor authority and fence disagree")
	}
	return resolveProvisionAuthorityTx(ctx, tx, actor.Authority, actor.ConsoleProof)
}

// RevokeAuthority revokes a member's own authority or a tenant administrator's target.
func (t *Tenant) RevokeAuthority(ctx context.Context, actor Actor, target capability.AuthorityRef) error {
	return t.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockAuthorityTx(ctx, tx, []capability.AuthorityRef{actor.Authority, target}, target.ID); err != nil {
			return err
		}
		p, err := resolveActorTx(ctx, tx, actor)
		if err != nil {
			return err
		}
		if p.Capability.Team != string(t.team) || p.Capability.Binding.Kind != capability.BindingNone {
			return authorityError(apierr.Forbidden, "revocation requires unbound tenant authority")
		}
		targetNode, err := readAuthorityTx(ctx, tx, target, true)
		if err != nil {
			return err
		}
		if targetNode.principal.Capability.Team != string(t.team) {
			return authorityError(apierr.Forbidden, "revocation target belongs to another team")
		}
		if err := machineAdministrationTx(ctx, tx, p, targetNode.principal.Capability); err != nil {
			return err
		}
		if !slices.Contains(p.Capability.Grants, capability.TeamAdmin) && p.Authority != target {
			if target.Kind != capability.AuthorityCapability {
				return bindingRefusal()
			}
			if err := bindingTokenTx(ctx, tx, p.Capability, capability.Target{Team: string(t.team), TokenPrefix: target.ID}); err != nil {
				return err
			}
		}
		if err := revokeAuthorityTx(ctx, tx, target); err != nil {
			return err
		}
		return nil
	})
}

// RevokeAuthority limits deployment revocation to service and operator roots.
func (o *Operator) RevokeAuthority(ctx context.Context, actor Actor, target capability.AuthorityRef) error {
	return o.s.inTx(ctx, func(tx *storeTx) error {
		if err := lockAuthorityTx(ctx, tx, []capability.AuthorityRef{actor.Authority, target}, target.ID); err != nil {
			return err
		}
		p, err := resolveActorTx(ctx, tx, actor)
		if err != nil {
			return err
		}
		if !(capability.DeploymentAuthority(p) && slices.Contains(p.Capability.Grants, capability.OperatorTokens)) && !(p.Class == capability.CredentialConsole && slices.Contains(p.Capability.Grants, capability.OperatorConsole)) {
			return authorityError(apierr.Forbidden, "deployment token authority is required")
		}
		node, err := readAuthorityTx(ctx, tx, target, true)
		if err != nil {
			return err
		}
		c := node.principal.Capability
		if target.Kind != capability.AuthorityCapability || c.Parent != nil || c.Team != "" || (c.Subject.Kind != capability.SubjectService && c.Subject.Kind != capability.SubjectOperator) {
			return authorityError(apierr.Forbidden, "target is not a deployment root")
		}
		if err := revokeAuthorityTx(ctx, tx, target); err != nil {
			return err
		}
		return nil
	})
}

func revokeAuthorityTx(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) error {
	now := tx.now()
	if ref.Kind == capability.AuthorityCapability {
		record, err := loadCapabilityRecordTx(ctx, tx, ref.ID)
		if err != nil {
			return err
		}
		record.Value.RevokedAt = &now
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE capabilities SET revoked_at = ?, authority_json = ? WHERE prefix = ?", now.UnixNano(), body, ref.ID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET revoked_at = ? WHERE hash = ?", now.UnixNano(), ref.ID); err != nil {
			return err
		}
	}
	return nil
}

var _ capability.AuthorityResolver = (*storeTx)(nil)
