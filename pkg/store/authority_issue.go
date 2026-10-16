package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// Issue persists one capability within its intake, claim or exchange transaction.
// Raw must not leave the transaction until its commit succeeds.
func (s *Store) Issue(ctx context.Context, tx *storeTx, request capability.IssueRequest) (capability.Issued, error) {
	return s.issueTx(ctx, tx, request, nil)
}

func (s *Store) issueTx(ctx context.Context, tx *storeTx, request capability.IssueRequest, prospective *awardRow) (capability.Issued, error) {
	if tx == nil || tx.store != s {
		return capability.Issued{}, authorityError(apierr.BadRequest, "issuance requires this store's transaction")
	}
	variants := 0
	for _, present := range []bool{request.Provision != nil, request.Delegate != nil, request.Award != nil, request.Exchange != nil} {
		if present {
			variants++
		}
	}
	if variants != 1 {
		return capability.Issued{}, authorityError(apierr.BadRequest, "issuance requires exactly one variant")
	}
	if request.Provision != nil {
		if err := capability.ValidateProvisionSeed(*request.Provision); err != nil {
			return capability.Issued{}, err
		}
	}
	var c capability.Capability
	var err error
	switch {
	case request.Kind == capability.Award && request.Award != nil:
		c, err = awardCapabilityTx(ctx, tx, *request.Award, prospective)
	case request.Kind == capability.Exchange && request.Exchange != nil:
		c, err = exchangeCapabilityTx(ctx, tx, *request.Exchange)
	case request.Kind == capability.Delegate && request.Provision != nil:
		c, err = provisionCapabilityTx(ctx, tx, *request.Provision)
	case request.Kind == capability.Delegate && request.Delegate != nil:
		c, err = delegateCapabilityTx(ctx, tx, *request.Delegate)
	default:
		return capability.Issued{}, authorityError(apierr.BadRequest, "issuance kind does not match variant")
	}
	if err != nil {
		return capability.Issued{}, err
	}
	seed := ""
	if request.Provision != nil {
		seed = request.Provision.Seed
	}
	issued, err := persistIssuedCapabilityTx(ctx, tx, c, seed, request.Provision)
	if err != nil {
		return capability.Issued{}, err
	}
	c = issued.Capability
	if request.Provision != nil {
		if err := recordProvisionCreatorTx(ctx, tx, *request.Provision, c); err != nil {
			return capability.Issued{}, err
		}
		if request.Provision.Kind == capability.ProvisionBootstrap && tx.rotatingRoot == nil {
			result, err := tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='bootstrap_admin'`, []byte(c.Prefix))
			if err != nil {
				return capability.Issued{}, err
			}
			if err := requireOneRow(result, "bootstrap gate"); err != nil {
				return capability.Issued{}, err
			}
		}
		if err := finishBootstrapSetupTx(ctx, tx, c); err != nil {
			return capability.Issued{}, err
		}
	}
	if request.Exchange != nil {
		if err := bindExchangedUnitTx(ctx, tx, c, request.Exchange.BootstrapPrefix); err != nil {
			return capability.Issued{}, err
		}
	}
	if request.Award != nil {
		if err := persistUnitBillingTx(ctx, tx, c); err != nil {
			return capability.Issued{}, err
		}
	}
	return issued, nil
}

func encodeAwardUnit(unit UnitRef) (string, error) {
	if unit.Kind == capability.UnitTrigger || unit.Kind == capability.UnitPlan {
		return unit.RunID, nil
	}
	body, err := json.Marshal(unit)
	return string(body), err
}

func decodeAwardUnit(kind capability.UnitKind, id string) (UnitRef, error) {
	if kind == capability.UnitTrigger || kind == capability.UnitPlan {
		return UnitRef{Kind: kind, RunID: id}, nil
	}
	var unit UnitRef
	if err := json.Unmarshal([]byte(id), &unit); err != nil {
		return unit, authorityError(apierr.BadRequest, "invalid award unit identity")
	}
	if unit.Kind != kind || unit.RunID == "" || unit.NodeID == "" || (kind == capability.UnitEval && unit.SourceGeneration <= 0) {
		return unit, authorityError(apierr.BadRequest, "invalid award unit identity")
	}
	return unit, nil
}

type awardRow struct {
	team, run, node, holder, prefix, repo, display, ref, sha string
	generation, attempt, created                             int64
	expiry, lease, cancel                                    sql.NullInt64
	request                                                  []byte
}

func loadAwardRowTx(ctx context.Context, tx *storeTx, spec capability.AwardSpec) (awardRow, error) {
	unit, err := decodeAwardUnit(spec.Unit, spec.UnitID)
	if err != nil {
		return awardRow{}, err
	}
	row := awardRow{run: unit.RunID, node: unit.NodeID}
	switch spec.Unit {
	case capability.UnitTrigger, capability.UnitPlan:
		err = tx.QueryRowContext(ctx, "SELECT team,COALESCE(claimed_by,''),capability_prefix,generation,expires_at,lease_expires_at,created_at,cancel_requested_at FROM triggers WHERE id = ? AND unit_kind = ?"+tx.forUpdate(), unit.RunID, string(spec.Unit)).Scan(&row.team, &row.holder, &row.prefix, &row.generation, &row.expiry, &row.lease, &row.created, &row.cancel)
	case capability.UnitNode:
		err = tx.QueryRowContext(ctx, "SELECT team,COALESCE(claimed_by,''),capability_prefix,generation,attempt,lease_expires_at,cancel_requested_at FROM nodes WHERE run_id = ? AND node_id = ? AND status <> 'done'"+tx.forUpdate(), unit.RunID, unit.NodeID).Scan(&row.team, &row.holder, &row.prefix, &row.generation, &row.attempt, &row.lease, &row.cancel)
	case capability.UnitEval:
		err = tx.QueryRowContext(ctx, "SELECT team,COALESCE(claimed_by,''),capability_prefix,generation,expires_at,lease_expires_at FROM eval_units WHERE run_id = ? AND source_node_id = ? AND source_generation = ? AND status IN ('pending','claimed','running')"+tx.forUpdate(), unit.RunID, unit.NodeID, unit.SourceGeneration).Scan(&row.team, &row.holder, &row.prefix, &row.generation, &row.expiry, &row.lease)
	default:
		return row, authorityError(apierr.BadRequest, "unknown award unit")
	}
	if err != nil {
		return row, err
	}
	var status string
	var runCancel sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT status,cancel_requested_at,repo_key,repo_display,git_branch,git_sha,request_json FROM runs WHERE team = ? AND id = ?"+tx.forShare(), row.team, row.run).Scan(&status, &runCancel, &row.repo, &row.display, &row.ref, &row.sha, &row.request)
	if err != nil {
		return row, err
	}
	if runCancel.Valid || row.cancel.Valid || (status != "pending" && status != "running") {
		return row, authorityError(apierr.LockHeld, "award run is no longer executable")
	}
	if !row.expiry.Valid && (spec.Unit == capability.UnitNode || spec.Unit == capability.UnitEval) {
		err = tx.QueryRowContext(ctx, "SELECT expires_at FROM triggers WHERE team = ? AND id = ?", row.team, row.run).Scan(&row.expiry)
		if err != nil && err != sql.ErrNoRows {
			return row, err
		}
	}
	return row, nil
}

func awardCapabilityTx(ctx context.Context, tx *storeTx, spec capability.AwardSpec, prospective *awardRow) (capability.Capability, error) {
	policy, ok := awardPolicy(spec.Purpose)
	if !ok || policy.Unit != spec.Unit || (spec.Delivery != capability.DeliveryDirect && spec.Delivery != capability.DeliveryBootstrap) {
		return capability.Capability{}, authorityError(apierr.BadRequest, "invalid award purpose")
	}
	chain, err := authorityChainTx(ctx, tx, spec.Claimant, true)
	if err != nil {
		return capability.Capability{}, err
	}
	claimant := chain[0].principal
	claimOK := false
	for _, g := range policy.ClaimGrants {
		if slices.Contains(claimant.Capability.Grants, g) {
			claimOK = true
		}
	}
	if !claimOK {
		return capability.Capability{}, authorityError(apierr.Forbidden, "claimant lacks the award grant")
	}
	launcher := claimant.Capability.Subject.Kind == capability.SubjectService && claimant.Capability.Binding.Kind == capability.BindingNone && slices.Contains(claimant.Capability.Grants, capability.ClaimsLaunch)
	if spec.Delivery == capability.DeliveryBootstrap && (!launcher || !slices.Contains(capability.BootstrapUnits, spec.Unit)) {
		return capability.Capability{}, authorityError(apierr.Forbidden, "bootstrap requires launcher authority")
	}
	row, err := loadAwardRowTx(ctx, tx, spec)
	if err != nil {
		return capability.Capability{}, err
	}
	if row.display == "" {
		var sealed runspec.SealedRequest
		if err := json.Unmarshal(row.request, &sealed); err != nil {
			return capability.Capability{}, err
		}
		row.display = sealed.Request.Git.Repository.Display
	}
	if err := checkRepositoryClaimScope(chain, capability.RepositoryIdentity{Key: row.repo, Display: row.display}); err != nil {
		return capability.Capability{}, err
	}
	if err := checkPushClaimScope(chain, row.repo, row.ref, row.sha); err != nil {
		return capability.Capability{}, err
	}
	if prospective != nil {
		// safety: the locked row supplies scope; the claim transaction supplies only its next fence.
		if prospective.team != row.team || prospective.run != row.run || prospective.node != row.node || row.holder != "" || row.prefix != "" || prospective.generation != row.generation+1 {
			return capability.Capability{}, holderRefusal()
		}
		row.holder, row.generation, row.lease = prospective.holder, prospective.generation, prospective.lease
	}
	if !launcher && claimant.Capability.Team != row.team {
		return capability.Capability{}, authorityError(apierr.Forbidden, "claimant belongs to another team")
	}
	if claimant.Capability.Binding.Kind != capability.BindingNone && claimant.Capability.Binding.RunID != "" && claimant.Capability.Binding.RunID != row.run {
		return capability.Capability{}, authorityError(apierr.Forbidden, "claimant is bound to another run")
	}
	if row.prefix != "" {
		return capability.Capability{}, authorityError(apierr.LockHeld, "unit already has an awarded capability")
	}
	here := spec.Purpose == capability.PurposeHere
	if here {
		if spec.HereRunID != row.run || spec.Unit != capability.UnitTrigger {
			return capability.Capability{}, authorityError(apierr.BadRequest, "here award names another run")
		}
	} else if row.generation <= 0 || row.holder != claimant.Capability.Subject.ID || !row.lease.Valid || row.lease.Int64 <= tx.now().UnixNano() {
		return capability.Capability{}, authorityError(apierr.LockHeld, "unit is not held by claimant")
	}
	parent := chain[len(chain)-1].principal
	for _, node := range chain {
		if node.principal.Capability.Purpose == capability.PurposeHere {
			parent = node.principal
			break
		}
	}
	if here {
		parent = claimant
	}
	c := capability.Capability{Team: row.team, Subject: claimant.Capability.Subject, Audience: capability.Controller, Grants: slices.Clone(policy.Grants), Issuance: capability.Award, Unit: spec.Unit, UnitID: spec.UnitID, Purpose: spec.Purpose, Delivery: spec.Delivery, Parent: &parent.Authority, ReservedPools: slices.Clone(parent.Capability.ReservedPools), Repositories: []string{row.repo}}
	c.Binding = capability.Binding{Kind: policy.Bindings[0], RunID: row.run, NodeID: row.node, Generation: row.generation, Holder: row.holder, Attempt: 0}
	if row.expiry.Valid {
		deadline := time.Unix(0, row.expiry.Int64).UTC()
		c.Binding.Deadline = &deadline
		c.ExpiresAt = &deadline
	}
	if parent.Capability.ExpiresAt != nil && (c.ExpiresAt == nil || parent.Capability.ExpiresAt.Before(*c.ExpiresAt)) {
		c.ExpiresAt = parent.Capability.ExpiresAt
	}
	if c.ExpiresAt == nil && !here && parent.Capability.Subject.Kind != capability.SubjectLocal && parent.Capability.Purpose != capability.PurposeHere {
		return capability.Capability{}, authorityError(apierr.Unavailable, "awarded unit deadline is unavailable")
	}
	if here {
		by := time.Unix(0, row.created).UTC().Add(limits.ClaimWindow)
		c.Binding.ClaimBy = &by
		c.Binding.Reservation = "here:" + row.run
		if row.expiry.Valid {
			expiry := time.Unix(0, row.expiry.Int64).UTC().Add(limits.ClaimWindow)
			c.ExpiresAt = &expiry
			if parent.Capability.ExpiresAt != nil && parent.Capability.ExpiresAt.Before(expiry) {
				c.ExpiresAt = parent.Capability.ExpiresAt
			}
		}
	}
	if spec.Unit == capability.UnitEval {
		unit, _ := decodeAwardUnit(spec.Unit, spec.UnitID)
		c.Binding.Generation = unit.SourceGeneration
		c.Binding.Expansion = &capability.LeaseRef{Kind: spec.Unit, ID: spec.UnitID, Generation: row.generation, Holder: row.holder}
	}
	c.Data = awardDataScope(row, c)
	if spec.Delivery == capability.DeliveryBootstrap {
		c.Grants = slices.Clone(capability.BootstrapGrants)
	}
	reservation, err := newCreditID("award")
	if err != nil {
		return capability.Capability{}, err
	}
	start := capability.BillingAtAward
	executor := "runner"
	if launcher {
		start = capability.BillingAtStart
		executor = "cloud"
	}
	if parent.Capability.Subject.Kind == capability.SubjectLocal {
		executor = "local"
	}
	if here || parent.Capability.Purpose == capability.PurposeHere {
		executor = "here"
	}
	c.RootMetered = meteredPlacement(claimant.Capability, parent.Capability)
	c.Billing = &capability.BillingProvenance{PayerTeam: row.team, Metered: c.RootMetered, ExecutorClass: executor, ReservationID: reservation, Start: start, Unit: billingUnitKind(spec.Unit)}
	return c, nil
}

func awardDataScope(row awardRow, c capability.Capability) *capability.DataScope {
	scope := &capability.DataScope{Use: capability.DataClaim, RepositoryKey: row.repo, WriteRef: "manual:" + row.ref, RunID: row.run, NodeID: row.node, Attempt: row.attempt, Generation: c.Binding.Generation}
	var sealed runspec.SealedRequest
	if json.Unmarshal(row.request, &sealed) == nil && sealed.Request.Git.App.Set && sealed.Request.Git.App.Value.Ref == row.ref && sealed.Request.Git.App.Value.SHA == row.sha {
		scope.WriteRef = row.ref
	}
	scope.ReadRefs = []string{scope.WriteRef}
	if c.Unit == capability.UnitEval {
		scope.Use = capability.DataExpansion
		scope.WriteRef = ""
	}
	return scope
}

func exchangeCapabilityTx(ctx context.Context, tx *storeTx, spec capability.ExchangeSpec) (capability.Capability, error) {
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: spec.BootstrapPrefix}
	p, err := tx.ResolveAuthority(ctx, ref)
	if err != nil {
		return capability.Capability{}, err
	}
	c := p.Capability
	if c.Delivery != capability.DeliveryBootstrap || c.Parent == nil || !slices.Contains(c.Grants, capability.ClaimExchange) {
		return capability.Capability{}, authorityError(apierr.Forbidden, "authority is not an exchangeable bootstrap")
	}
	row, err := loadAwardRowTx(ctx, tx, capability.AwardSpec{Unit: c.Unit, UnitID: c.UnitID})
	if err != nil {
		return capability.Capability{}, err
	}
	generation := c.Binding.Generation
	if c.Binding.Expansion != nil {
		generation = c.Binding.Expansion.Generation
	}
	if row.prefix != spec.BootstrapPrefix || row.holder != c.Binding.Holder || row.generation != generation || !row.lease.Valid || row.lease.Int64 <= tx.now().UnixNano() {
		return capability.Capability{}, authorityError(apierr.LockHeld, "bootstrap unit fence has ended")
	}
	if err := revokeAuthorityTx(ctx, tx, ref); err != nil {
		return capability.Capability{}, err
	}
	policy, ok := awardPolicy(c.Purpose)
	if !ok {
		return capability.Capability{}, authorityError(apierr.IntegrityFailure, "unknown bootstrap purpose")
	}
	c.Issuance = capability.Exchange
	c.Delivery = capability.DeliveryDirect
	c.Grants = slices.Clone(policy.Grants)
	c.RevokedAt = nil
	return c, nil
}

func bindExchangedUnitTx(ctx context.Context, tx *storeTx, c capability.Capability, bootstrap string) error {
	var result sql.Result
	var err error
	switch c.Unit {
	case capability.UnitTrigger, capability.UnitPlan:
		result, err = tx.ExecContext(ctx, "UPDATE triggers SET capability_prefix = ? WHERE team = ? AND id = ? AND capability_prefix = ? AND generation = ?", c.Prefix, c.Team, c.Binding.RunID, bootstrap, c.Binding.Generation)
	case capability.UnitNode:
		result, err = tx.ExecContext(ctx, "UPDATE nodes SET capability_prefix = ? WHERE team = ? AND run_id = ? AND node_id = ? AND capability_prefix = ? AND generation = ?", c.Prefix, c.Team, c.Binding.RunID, c.Binding.NodeID, bootstrap, c.Binding.Generation)
	case capability.UnitEval:
		result, err = tx.ExecContext(ctx, "UPDATE eval_units SET capability_prefix = ? WHERE team = ? AND run_id = ? AND source_node_id = ? AND source_generation = ? AND capability_prefix = ? AND generation = ?", c.Prefix, c.Team, c.Binding.RunID, c.Binding.NodeID, c.Binding.Generation, bootstrap, c.Binding.Expansion.Generation)
	default:
		return authorityError(apierr.BadRequest, "unknown exchange unit")
	}
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return authorityError(apierr.LockHeld, "bootstrap claim changed during exchange")
	}
	return nil
}

func persistIssuedCapabilityTx(ctx context.Context, tx *storeTx, c capability.Capability, seed string, provision ...*capability.ProvisionSpec) (capability.Issued, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return capability.Issued{}, err
	}
	prefix := capability.ControllerPrefix
	if c.Audience == capability.NodeHost {
		prefix = capability.NodeHostPrefix
	}
	raw := prefix + base64.RawURLEncoding.EncodeToString(secret[:])
	if seed != "" {
		raw = seed
	}
	c.Prefix = raw[:16]
	c.Digest = sha256.Sum256([]byte(raw))
	c.IssuedAt = tx.now()
	var origin *CapabilityOrigin
	if len(provision) > 0 && provision[0] != nil {
		origin = &CapabilityOrigin{Kind: provision[0].Kind, RegistrationID: provision[0].RegistrationID}
	}
	if err := persistCapabilityTx(ctx, tx, c, origin); err != nil {
		return capability.Issued{}, err
	}
	return capability.Issued{Raw: raw, Capability: c}, nil
}
