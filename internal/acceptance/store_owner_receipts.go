package acceptance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreOwnerReceiptBoundary(ctx context.Context, root string) error {
	for _, kind := range []capability.UnitKind{capability.UnitTrigger, capability.UnitPlan} {
		if err := checkStoreOwnerReceipt(ctx, filepath.Join(root, string(kind)+".db"), kind); err != nil {
			return fmt.Errorf("%s owner receipt: %w", kind, err)
		}
	}
	return nil
}

func checkStoreOwnerReceipt(ctx context.Context, path string, kind capability.UnitKind) (err error) {
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, award, err := seedStoreOwnerReceipt(ctx, s.DB(), kind)
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	ticket, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	if ticket.Provenance != blob.ProvenanceLocal {
		return fmt.Errorf("non-cloud award assigned upload provenance %s", ticket.Provenance)
	}
	if trust, err := tenant.TrustLocalBuilds(ctx); err != nil || trust {
		return fmt.Errorf("local binaries trusted without opt-in: %v: %v", trust, err)
	}
	if err := tenant.SetTrustLocalBuilds(ctx, true); err != nil {
		return err
	}
	query := blob.HasManyRequest{Authority: request.Intent.Authority, Digests: []blob.Digest{ticket.Blob.Digest}}
	if presence, err := tenant.ReadableBlobs(ctx, query); err != nil || len(presence.Present) != 0 {
		return fmt.Errorf("reservation exposed bytes: %+v: %v", presence, err)
	}
	commit := store.VerifiedUploadCommit{TicketID: ticket.ID, Authority: request.Intent.Authority, Verified: ticket.Blob, ReplicaID: "owner"}
	bad := commit
	bad.Verified.Size++
	if _, err := tenant.CommitBlobUpload(ctx, bad); !errors.Is(err, blob.ErrIntegrity) {
		return fmt.Errorf("conflicting verification accepted: %v", err)
	}
	if presence, err := tenant.ReadableBlobs(ctx, query); err != nil || len(presence.Present) != 0 {
		return fmt.Errorf("failed verification exposed bytes: %+v: %v", presence, err)
	}
	for range 2 {
		receipt, err := tenant.CommitBlobUpload(ctx, commit)
		if err != nil || receipt.Identity != request.Intent.Identity {
			return fmt.Errorf("committed owner identity=%+v: %v", receipt, err)
		}
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(path)
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if trust, err := tenant.TrustLocalBuilds(ctx); err != nil || !trust {
		return fmt.Errorf("reopened build trust lost opt-in: %v: %v", trust, err)
	}
	if presence, err := tenant.ReadableBlobs(ctx, query); err != nil || !slices.Equal(presence.Present, query.Digests) {
		return fmt.Errorf("reopened receipt lost authority: %+v: %v", presence, err)
	}
	persisted, err := tenant.LookupBlobUpload(ctx, request.Intent.Authority, ticket.ID)
	if err != nil || persisted.Identity != request.Intent.Identity || persisted.Identity.Attempt.Set || persisted.CommittedAt == nil || persisted.Provenance != blob.ProvenanceLocal {
		return fmt.Errorf("reopened owner ticket=%+v: %v", persisted, err)
	}
	var jobs int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE team=? AND run_id='upload'", store.DefaultTeam).Scan(&jobs); err != nil || jobs != 0 {
		return fmt.Errorf("owner upload required synthetic jobs: %d: %v", jobs, err)
	}
	job := award
	job.Prefix, job.Digest = "unrelated-job", sha256.Sum256([]byte("unrelated-job"))
	job.Unit, job.UnitID, job.Purpose = capability.UnitNode, "job", capability.PurposeWork
	job.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "upload", NodeID: "job", Holder: "publisher", Generation: 11}
	billing := *award.Billing
	billing.ReservationID, billing.Unit = "job-reservation", capability.BillingJob
	job.Billing = &billing
	if err := seedStoreDataAward(ctx, s.DB(), job); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,claimed_by,capability_prefix,generation,attempt,lease_expires_at,spec_json,execution_json) VALUES (?,'upload','job','running','publisher',?,11,1,?,?,?)`, store.DefaultTeam, job.Prefix, time.Now().Add(time.Hour).UnixNano(), []byte(`{}`), []byte(`{}`)); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES (?,'upload','job',1,11,'publisher',?,'runner','publisher','remote',1)`, store.DefaultTeam, job.Prefix); err != nil {
		return err
	}
	query.Authority = blob.Authority{CapabilityRef: job.Prefix}
	if presence, err := tenant.ReadableBlobs(ctx, query); err != nil || len(presence.Present) != 0 {
		return fmt.Errorf("job inherited owner receipt: %+v: %v", presence, err)
	}
	authorized := capability.Authorized{Principal: capability.Principal{Capability: job}}
	for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataRecord} {
		if err := tenant.CheckDataAccess(ctx, authorized, capability.DataTarget{Operation: operation, Digest: string(ticket.Blob.Digest)}); !errors.Is(err, blob.ErrDigestNotReadable) {
			return fmt.Errorf("job inherited owner receipt through %s: %v", operation, err)
		}
	}
	if _, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{TicketID: ticket.ID, Authority: query.Authority, Verified: ticket.Blob, ReplicaID: "job"}); err == nil {
		return errors.New("job adopted the owner's upload ticket")
	}
	return checkStorePresenceUsage(ctx, tenant, s.DB())
}

func seedStoreOwnerReceipt(ctx context.Context, db *sql.DB, kind capability.UnitKind) (store.UploadReservationRequest, capability.Capability, error) {
	request, err := seedStoreUploadBoundary(ctx, db)
	if err != nil {
		return request, capability.Capability{}, err
	}
	now := time.Now().UTC()
	billingUnit := capability.BillingUnit(kind)
	if kind == capability.UnitTrigger {
		billingUnit = capability.BillingHost
	}
	award := capability.Capability{
		Prefix: "owner-award", Digest: sha256.Sum256([]byte("owner-award")), Team: string(store.DefaultTeam),
		Subject: capability.Subject{Kind: capability.SubjectMachine, ID: "publisher"}, Audience: capability.Controller,
		Grants: []capability.Grant{capability.DataRead, capability.DataWrite, capability.LogTail}, Issuance: capability.Award,
		Parent: &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: request.Intent.Authority.CapabilityRef},
		Unit:   kind, UnitID: "upload", Purpose: capability.AwardPurpose(kind), Delivery: capability.DeliveryDirect,
		Binding:  capability.Binding{Kind: capability.BindingTrigger, RunID: "upload", Generation: 7, Holder: "publisher"},
		Billing:  &capability.BillingProvenance{PayerTeam: string(store.DefaultTeam), ExecutorClass: "runner", ReservationID: "owner-reservation", Start: capability.BillingAtAward, Unit: billingUnit},
		Data:     &capability.DataScope{Use: capability.DataClaim, RepositoryKey: "gh:1", RunID: "upload", ReadRefs: []string{"manual:"}, WriteRef: "manual:"},
		IssuedAt: now, ReservedPools: []string{}, Repositories: []string{},
	}
	if err := seedStoreDataAward(ctx, db, award); err != nil {
		return request, award, err
	}
	if _, err := db.ExecContext(ctx, "UPDATE runs SET intake_mode='execute' WHERE team=? AND id='upload'", store.DefaultTeam); err != nil {
		return request, award, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,lease_expires_at,expires_at,generation,claimed_by,capability_prefix,needs_labels,prefers_labels) VALUES (?,'upload','claimed',?,1,1,?,?,7,'publisher',?,?,?)`, store.DefaultTeam, kind, now.Add(time.Hour).UnixNano(), now.Add(time.Hour).UnixNano(), award.Prefix, []byte(`[]`), []byte(`[]`)); err != nil {
		return request, award, err
	}
	request.Intent.Authority = blob.Authority{CapabilityRef: award.Prefix}
	request.Intent.Identity = blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: award.Team, RunID: "upload", Principal: "publisher", CapabilityRef: award.Prefix, Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadUnitKind(kind), ID: "upload", Generation: 7}}}
	return request, award, nil
}

// safety: preseeded award facts isolate transactional receipts from issuance and physical transfer.
func seedStoreDataAward(ctx context.Context, db *sql.DB, c capability.Capability) error {
	record := store.CapabilityRecord{Version: 1, Value: c}
	if err := record.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	grants, err := json.Marshal(c.Grants)
	if err != nil {
		return err
	}
	var node any
	if c.Binding.NodeID != "" {
		node = c.Binding.NodeID
	}
	columns := "prefix,digest,team,subject_kind,subject_id,audience,grants,issuance,authority_version,authority_json,parent_kind,parent_id,award_purpose,delivery_purpose,unit_id,billing_unit,bind_kind,bind_run,bind_node,bind_generation,bind_holder,reserved_pools,created_at,created_by,repo_scope,payer_team,executor_class,reservation_id,billing_start,unit_kind,repo_key,ref_scope"
	values := []any{c.Prefix, hex.EncodeToString(c.Digest[:]), c.Team, c.Subject.Kind, c.Subject.ID, c.Audience, grants, c.Issuance, 1, body, c.Parent.Kind, c.Parent.ID, c.Purpose, c.Delivery, c.UnitID, c.Billing.Unit, c.Binding.Kind, c.Binding.RunID, node, c.Binding.Generation, c.Binding.Holder, []byte(`[]`), c.IssuedAt.UnixNano(), c.Subject.ID, []byte(`[]`), c.Billing.PayerTeam, c.Billing.ExecutorClass, c.Billing.ReservationID, c.Billing.Start, c.Unit, c.Data.RepositoryKey, c.Data.WriteRef}
	_, err = db.ExecContext(ctx, "INSERT INTO capabilities("+columns+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")+")", values...)
	return err
}
