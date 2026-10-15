package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStoreLargePresenceBoundary(ctx context.Context, root string) (err error) {
	path := filepath.Join(root, "store-large-presence.db")
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := seedStorePresenceAttempts(ctx, s.DB())
	if err != nil {
		return err
	}
	request.Intent.Authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: "a", Attempt: 1, Generation: 7}}
	request.Intent.Identity.Attempt = request.Intent.Authority.Attempt
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	if _, err := s.DB().ExecContext(ctx, "INSERT INTO teams(name,created_at,updated_at) VALUES ('foreign',1,1)"); err != nil {
		return err
	}
	var digests []blob.Digest
	readable := map[blob.Digest]bool{}
	for i := range 1025 {
		sum := sha256.Sum256(fmt.Appendf(nil, "presence-%d", i))
		digest := blob.Digest(hex.EncodeToString(sum[:]))
		digests = append(digests, digest)
		switch i % 4 {
		case 0:
			request.ID = fmt.Sprintf("presence_%d", i)
			request.Intent.Digest, request.Intent.Size = digest, 1
			ticket, err := tenant.ReserveBlobUpload(ctx, request)
			if err != nil {
				return err
			}
			receipt, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{TicketID: ticket.ID, Authority: request.Intent.Authority, Verified: ticket.Blob, ReplicaID: "fixture"})
			if err != nil || receipt.Identity != request.Intent.Identity {
				return fmt.Errorf("large presence upload %d: %+v: %v", i, receipt, err)
			}
			readable[digest] = true
		case 1, 2:
			team := store.DefaultTeam
			if i%4 == 2 {
				team = "foreign"
			}
			if _, err := s.DB().ExecContext(ctx, "INSERT INTO blobs(team,digest,size,created_at) VALUES (?,?,1,1)", team, digest); err != nil {
				return err
			}
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
	for range 2 {
		query := blob.HasManyRequest{Authority: request.Intent.Authority, Digests: append(slices.Clone(digests), digests...), Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}
		var want []blob.Digest
		for _, digest := range digests {
			if readable[digest] {
				want = append(want, digest)
			}
		}
		presence, err := tenant.ReadableBlobs(ctx, query)
		if err != nil || !slices.Equal(presence.Present, want) {
			return fmt.Errorf("large presence returned %d readable digests, want %d: %v", len(presence.Present), len(want), err)
		}
		query.Budget.Deadline = time.Unix(1, 0)
		if presence, err := tenant.ReadableBlobs(ctx, query); !errors.Is(err, blob.ErrBudget) || len(presence.Present) != 0 {
			return fmt.Errorf("expired large presence returned a partial success: %+v: %v", presence, err)
		}
		slices.Reverse(digests)
	}
	usage, err := tenant.StorageUsageFor(ctx, "upload", store.StorageMonth(time.Now()))
	if err != nil || usage.RunBytes != 257 || usage.MonthBytes != 257 || usage.RunObjects != 257 || usage.MonthObjects != 257 {
		return fmt.Errorf("large presence altered upload accounting: %+v: %v", usage, err)
	}
	for _, table := range []string{"team_download_day", "egress_usage"} {
		var count int
		if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE team=?", store.DefaultTeam).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("large presence consumed %s bytes", table)
		}
	}
	return nil
}
