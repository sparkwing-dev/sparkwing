package acceptance_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func checkBlobUploadIsolation(t testing.TB, ctx context.Context, root string, objects blob.ObjectBackend, transfers blob.HTTPClient) (err error) {
	path := filepath.Join(root, "upload-isolation.db")
	s, err := teststore.OpenExisting(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := teststore.SeedPresence(ctx, s)
	if err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	random := rand.New(rand.NewPCG(29, 66))
	var total int64
	for i := range 32 {
		body := make([]byte, i*17)
		for j := range body {
			body[j] = byte(random.Uint32())
		}
		total += int64(len(body))
		request.ID = fmt.Sprintf("uploader-a-%d", i)
		request.Intent.Digest = blob.Digest(fmt.Sprintf("%x", sha256.Sum256(body)))
		request.Intent.Size = int64(len(body))
		request.Intent.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{
			Team: string(store.DefaultTeam), RunID: "upload", NodeID: "a", Attempt: 1, Generation: 7,
		}}
		request.Intent.Authority.Attempt = request.Intent.Identity.Attempt
		first, err := tenant.ReserveBlobUpload(ctx, request)
		if err != nil {
			return err
		}
		if err := uploadBoundaryObject(ctx, objects, transfers, first, body); err != nil {
			return err
		}
		presence, err := tenant.ReadableBlobs(ctx, blob.HasManyRequest{Authority: request.Intent.Authority, Digests: []blob.Digest{first.Blob.Digest}})
		if err != nil || len(presence.Present) != 0 {
			return fmt.Errorf("physical publication granted a receipt at case %d: %+v: %v", i, presence, err)
		}
		if _, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{
			TicketID: first.ID, Authority: request.Intent.Authority, Verified: first.Blob, ReplicaID: "a",
		}); err != nil {
			return err
		}
		request.ID = fmt.Sprintf("uploader-b-%d", i)
		request.Intent.Identity.Attempt.Value.NodeID = "b"
		request.Intent.Authority.Attempt = request.Intent.Identity.Attempt
		second, err := tenant.ReserveBlobUpload(ctx, request)
		if err != nil {
			return err
		}
		query := blob.HasManyRequest{Authority: request.Intent.Authority, Digests: []blob.Digest{second.Blob.Digest}}
		presence, err = tenant.ReadableBlobs(ctx, query)
		if err != nil || len(presence.Present) != 0 {
			return fmt.Errorf("reservation exposed uploader a's bytes at case %d: %+v: %v", i, presence, err)
		}
		if err := objects.CommitObject(ctx, second.StagingKey, second.Blob); !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("case %d accepted another ticket's final bytes without its own staged upload: %v", i, err)
		}
		if err := uploadBoundaryObject(ctx, objects, transfers, second, body); err != nil {
			return err
		}
		presence, err = tenant.ReadableBlobs(ctx, query)
		if err != nil || len(presence.Present) != 0 {
			return fmt.Errorf("physical deduplication granted a receipt at case %d: %+v: %v", i, presence, err)
		}
		receipt, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{
			TicketID: second.ID, Authority: request.Intent.Authority, Verified: second.Blob, ReplicaID: "b",
		})
		if err != nil || receipt.Identity != request.Intent.Identity {
			return fmt.Errorf("independent upload receipt at case %d: %+v: %v", i, receipt, err)
		}
		presence, err = tenant.ReadableBlobs(ctx, query)
		if err != nil || len(presence.Present) != 1 || presence.Present[0] != second.Blob.Digest {
			return fmt.Errorf("verified independent upload remains unreadable at case %d: %+v: %v", i, presence, err)
		}
		for _, ticket := range []store.UploadReservation{first, second} {
			if err := objects.DeleteObject(ctx, ticket.StagingKey); err != nil {
				return err
			}
		}
		if err := s.Close(); err != nil {
			return err
		}
		reopened, err := teststore.OpenExisting(path)
		if err != nil {
			return err
		}
		s = reopened
		tenant, err = s.ForTeam(ctx, store.DefaultTeam)
		if err != nil {
			return err
		}
		replayed, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{
			TicketID: second.ID, Authority: request.Intent.Authority, Verified: second.Blob, ReplicaID: "b",
		})
		if err != nil || replayed != receipt {
			return fmt.Errorf("settled receipt replay after staging cleanup at case %d: %+v: %v", i, replayed, err)
		}
		query.Authority.Attempt.Value.NodeID = "unrelated"
		presence, err = tenant.ReadableBlobs(ctx, query)
		if err != nil || len(presence.Present) != 0 {
			return fmt.Errorf("persisted receipt escaped its job at case %d: %+v: %v", i, presence, err)
		}
	}
	usage, err := tenant.StorageUsageFor(ctx, "upload", store.StorageMonth(time.Now()))
	if err != nil || usage.RunBytes != total || usage.MonthBytes != total || usage.RunObjects != 32 || usage.MonthObjects != 32 {
		return fmt.Errorf("independent upload deduplication: %+v: %v", usage, err)
	}
	return nil
}

func uploadBoundaryObject(ctx context.Context, objects blob.ObjectBackend, transfers blob.HTTPClient, ticket store.UploadReservation, body []byte) error {
	pending, err := stageBoundaryObject(ctx, objects, transfers, ticket.Blob, body, ticket.ID)
	if err != nil {
		return err
	}
	if pending != ticket.StagingKey {
		return blob.ErrIntegrity
	}
	return objects.CommitObject(ctx, pending, ticket.Blob)
}
