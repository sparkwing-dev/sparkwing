package acceptance

import (
	"context"
	"database/sql"
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

func checkStorePresenceBoundary(ctx context.Context, root string) (err error) {
	s, err := store.Open(filepath.Join(root, "store-presence.db"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := seedStorePresenceAttempts(ctx, s.DB())
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	bindJob := func(job string) {
		attempt := runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: job, Attempt: 1, Generation: 7}}
		request.Intent.Authority.Attempt = attempt
		request.Intent.Identity.Attempt = attempt
	}
	bindJob("a")
	first, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	commit := store.VerifiedUploadCommit{TicketID: first.ID, Authority: request.Intent.Authority, Verified: first.Blob, ReplicaID: "a"}
	receiptA, err := tenant.CommitBlobUpload(ctx, commit)
	if err != nil || receiptA.Identity != request.Intent.Identity {
		return fmt.Errorf("first uploader receipt=%+v: %v", receiptA, err)
	}
	foreign := blob.Digest(strings.Repeat("c", 64))
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,9,1)", store.DefaultTeam, foreign); err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)"); err != nil {
		return err
	}
	foreignTeam := blob.Digest(strings.Repeat("d", 64))
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('foreign',?,9,1)", foreignTeam); err != nil {
		return err
	}
	query := blob.HasManyRequest{Digests: []blob.Digest{first.Blob.Digest, foreign, blob.Digest(strings.Repeat("b", 64)), foreignTeam, first.Blob.Digest}}
	check := func(want []blob.Digest) error {
		query.Authority = request.Intent.Authority
		presence, err := tenant.ReadableBlobs(ctx, query)
		if err != nil || !slices.Equal(presence.Present, want) {
			return fmt.Errorf("presence for %s=%+v, want %v: %v", request.Intent.Identity.Attempt.Value.NodeID, presence, want, err)
		}
		return checkStorePresenceAccess(ctx, s, tenant, request.Intent.Authority, query.Digests, want)
	}
	if err := check([]blob.Digest{first.Blob.Digest}); err != nil {
		return err
	}
	bindJob("b")
	if err := check(nil); err != nil {
		return err
	}
	request.ID = "independent"
	second, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	if err := check(nil); err != nil {
		return fmt.Errorf("reservation granted a receipt: %w", err)
	}
	commit.TicketID, commit.Authority, commit.Verified, commit.ReplicaID = second.ID, request.Intent.Authority, second.Blob, "b"
	bad := commit
	bad.Verified.Size++
	if _, err := tenant.CommitBlobUpload(ctx, bad); err == nil {
		return errors.New("substituted verification evidence minted a receipt")
	}
	if err := check(nil); err != nil {
		return err
	}
	for range 2 {
		receiptB, err := tenant.CommitBlobUpload(ctx, commit)
		if err != nil || receiptB.Identity != request.Intent.Identity || receiptB.Identity == receiptA.Identity {
			return fmt.Errorf("independent uploader receipt=%+v: %v", receiptB, err)
		}
	}
	if err := s.Close(); err != nil {
		return err
	}
	reopened, err := store.Open(filepath.Join(root, "store-presence.db"))
	if err != nil {
		return err
	}
	s = reopened
	tenant, err = s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if err := check([]blob.Digest{first.Blob.Digest}); err != nil {
		return err
	}
	bindJob("unrelated")
	if err := check(nil); err != nil {
		return err
	}
	query.Authority, query.Budget.Deadline = request.Intent.Authority, time.Unix(1, 0)
	if _, err := tenant.ReadableBlobs(ctx, query); !errors.Is(err, blob.ErrBudget) {
		return fmt.Errorf("expired presence budget=%v", err)
	}
	return checkStorePresenceUsage(ctx, tenant, s.DB())
}

func checkStorePresenceAccess(ctx context.Context, s *store.Store, tenant *store.Tenant, authority blob.Authority, digests, present []blob.Digest) error {
	a := authority.Attempt.Value
	authorized := capability.Authorized{Principal: capability.Principal{Capability: capability.Capability{Prefix: authority.CapabilityRef, Team: a.Team}}}
	authorized.Principal.Authority = capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: authority.CapabilityRef}
	for _, operation := range []capability.DataOperation{capability.DataDownload, capability.DataHasMany, capability.DataLookup} {
		for _, digest := range digests {
			target := capability.DataTarget{Operation: operation, Digest: string(digest), RunID: a.RunID, NodeID: a.NodeID, Attempt: a.Attempt, Generation: a.Generation}
			err := tenant.CheckDataAccess(ctx, authorized, target)
			if slices.Contains(present, digest) {
				if err != nil {
					return fmt.Errorf("readable receipt refused by %s: %w", operation, err)
				}
			} else if !errors.Is(err, blob.ErrDigestNotReadable) {
				return fmt.Errorf("%s exposed unreadable digest: %v", operation, err)
			}
			for _, rule := range []capability.BindingRule{capability.BindData, capability.BindReadSet} {
				selected := capability.Target{Team: a.Team, RunID: a.RunID, NodeID: a.NodeID, Data: &target}
				err := s.CheckBinding(ctx, authorized.Principal, rule, selected)
				if slices.Contains(present, digest) && err != nil || !slices.Contains(present, digest) && !errors.Is(err, blob.ErrDigestNotReadable) {
					return fmt.Errorf("%s/%s binding exposed the wrong read set: %v", operation, rule, err)
				}
			}
		}
	}
	return nil
}

func seedStorePresenceAttempts(ctx context.Context, db *sql.DB) (store.UploadReservationRequest, error) {
	request, err := seedStoreUploadBoundary(ctx, db)
	if err != nil {
		return request, err
	}
	for _, job := range []string{"a", "b", "unrelated"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,attempt,generation,spec_json,execution_json) VALUES (?,'upload',?,'pending',1,7,?,?)`, store.DefaultTeam, job, []byte(`{}`), []byte(`{}`)); err != nil {
			return request, err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES (?,'upload',?,1,7,'publisher','upload-boundary','local','publisher','local',1)`, store.DefaultTeam, job); err != nil {
			return request, err
		}
	}
	return request, nil
}

func checkStorePresenceUsage(ctx context.Context, tenant *store.Tenant, db *sql.DB) error {
	usage, err := tenant.StorageUsageFor(ctx, "upload", store.StorageMonth(time.Now()))
	if err != nil || usage.RunBytes != 3 || usage.MonthBytes != 3 || usage.RunObjects != 1 || usage.MonthObjects != 1 {
		return fmt.Errorf("presence/upload usage=%+v: %v", usage, err)
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE team=?", store.DefaultTeam).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("presence metadata consumed %s budget", table)
		}
	}
	return nil
}
