package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkBlobManifestRecords(t testing.TB, ctx context.Context, root string, objects blob.ObjectBackend, transfers blob.HTTPClient) (err error) {
	path := filepath.Join(root, "manifest-records.db")
	s, err := teststore.OpenExisting(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	request, err := teststore.SeedPresence(ctx, teststore.SQL(t, s))
	if err != nil {
		return err
	}
	var raw []byte
	if err := teststore.SQL(t, s).QueryRowContext(ctx, "SELECT authority_json FROM capabilities WHERE prefix=?", request.Intent.Authority.CapabilityRef).Scan(&raw); err != nil {
		return err
	}
	var record store.CapabilityRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	record.Value.Data.WriteRef = "manual:main"
	record.Value.Data.ReadRefs = []string{"manual:main"}
	raw, err = json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE capabilities SET authority_json=?,ref_scope='manual:main' WHERE prefix=?", raw, record.Value.Prefix); err != nil {
		return err
	}
	if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE runs SET request_json=? WHERE id='upload'", []byte(`{"version":1,"request":{"git":{"ref":"main"}}}`)); err != nil {
		return err
	}
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		return err
	}
	records := tenant.BlobRecords(storage.BlobManifests{Objects: objects})
	random := rand.New(rand.NewPCG(29, 66))
	for i := range 12 {
		body := make([]byte, i*17)
		for j := range body {
			body[j] = byte(random.Uint32())
		}
		memberDigest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(body)))
		attempt := runspec.AttemptIdentity{Team: string(store.DefaultTeam), RunID: "upload", NodeID: "a", Attempt: 1, Generation: 7}
		request.Intent.Identity.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: attempt}
		request.Intent.Authority.Attempt = request.Intent.Identity.Attempt
		if err := settleManifestBlob(ctx, tenant, objects, transfers, request, fmt.Sprintf("member-a-%d", i), body); err != nil {
			return err
		}
		job := fmt.Sprintf("manifest-%d", i)
		if _, err := teststore.SQL(t, s).ExecContext(ctx, `INSERT INTO nodes(team,run_id,node_id,status,outcome,finished_at,attempt,generation,spec_json,execution_json) VALUES (?,'upload',?,'done','success',2,1,7,?,?)`, store.DefaultTeam, job, []byte(`{}`), []byte(`{}`)); err != nil {
			return err
		}
		if _, err := teststore.SQL(t, s).ExecContext(ctx, `INSERT INTO node_attempts(team,run_id,node_id,attempt,generation,holder,capability_prefix,executor_kind,executor_name,executor_location,started_at) VALUES (?,'upload',?,1,7,'publisher','upload-boundary','local','publisher','local',1)`, store.DefaultTeam, job); err != nil {
			return err
		}
		request.Intent.Identity.Attempt.Value.NodeID = job
		request.Intent.Authority.Attempt = request.Intent.Identity.Attempt
		manifest := blob.Manifest{Entries: []blob.ManifestEntry{{Path: "file", Digest: memberDigest, Size: int64(len(body)), Mode: 0o640}}}
		raw, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		manifestDigest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(raw)))
		unfinished := request
		unfinished.ID = fmt.Sprintf("unfinished-%d", i)
		unfinished.Intent.Digest, unfinished.Intent.Size = manifestDigest, int64(len(raw))
		_, refused := tenant.ReserveBlobUpload(ctx, unfinished)
		var illegal *store.ErrIllegalTransition
		if !errors.As(refused, &illegal) || illegal.Reason != store.ReasonHolderRights {
			return fmt.Errorf("case %d unfinished imported attempt reserved an upload: %v", i, refused)
		}
		var reserved int
		if err := teststore.SQL(t, s).QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads WHERE team=? AND id=?", store.DefaultTeam, unfinished.ID).Scan(&reserved); err != nil || reserved != 0 {
			return fmt.Errorf("case %d unfinished imported attempt left reservations=%d: %v", i, reserved, err)
		}
		if _, err := teststore.SQL(t, s).ExecContext(ctx, "UPDATE node_attempts SET finished_at=2,outcome='success' WHERE team=? AND run_id='upload' AND node_id=?", store.DefaultTeam, job); err != nil {
			return err
		}
		if err := settleManifestBlob(ctx, tenant, objects, transfers, request, fmt.Sprintf("manifest-%d", i), raw); err != nil {
			return err
		}
		members := []blob.Member{{Team: attempt.Team, ManifestDigest: manifestDigest, Digest: memberDigest, Path: "file", Size: int64(len(body)), Mode: 0o640}}
		commit := blob.CommitRecordRequest{
			Source: request.Intent.Identity, Authority: request.Intent.Authority, Digest: manifestDigest, Members: members,
			Key: blob.ResultRecordKey(request.Intent.Identity.Attempt.Value, "gh:1", blob.RefScope{Ref: "main"}, "fixture", blob.KindManifest),
		}
		forged := commit
		forged.Members = append([]blob.Member(nil), members...)
		forged.Members[0].Path = "forged"
		if _, err := records.CommitRecord(ctx, forged); !errors.Is(err, blob.ErrIntegrity) {
			return fmt.Errorf("case %d accepted substituted manifest metadata: %v", i, err)
		}
		if _, err := records.CommitRecord(ctx, commit); !errors.Is(err, blob.ErrDigestNotReadable) {
			return fmt.Errorf("case %d borrowed another job's member receipt: %v", i, err)
		}
		var count int
		if err := teststore.SQL(t, s).QueryRowContext(ctx, "SELECT COUNT(*) FROM blob_members WHERE manifest_digest=?", manifestDigest).Scan(&count); err != nil || count != 0 {
			return fmt.Errorf("case %d leaked refused members=%d: %v", i, count, err)
		}
		if err := settleManifestBlob(ctx, tenant, objects, transfers, request, fmt.Sprintf("member-b-%d", i), body); err != nil {
			return err
		}
		if _, err := records.CommitRecord(ctx, commit); err != nil {
			return fmt.Errorf("case %d refused its own member receipt: %w", i, err)
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
		records = tenant.BlobRecords(storage.BlobManifests{Objects: objects})
		hit, err := records.LookupRecord(ctx, blob.RecordLookup{Authority: commit.Authority, Key: commit.Key})
		if err != nil || !hit.Found || !reflect.DeepEqual(hit.Members, members) {
			return fmt.Errorf("case %d lost verified members after reopen: %+v: %v", i, hit, err)
		}
	}
	return nil
}

func settleManifestBlob(ctx context.Context, tenant *store.Tenant, objects blob.ObjectBackend, transfers blob.HTTPClient, request store.UploadReservationRequest, ticket string, body []byte) error {
	request.ID, request.Intent.Digest, request.Intent.Size = ticket, blob.Digest(fmt.Sprintf("%x", sha256.Sum256(body))), int64(len(body))
	reserved, err := tenant.ReserveBlobUpload(ctx, request)
	if err != nil {
		return err
	}
	pending, err := stageBoundaryObject(ctx, objects, transfers, reserved.Blob, body, ticket)
	if err != nil {
		return err
	}
	if pending != reserved.StagingKey {
		return blob.ErrIntegrity
	}
	if _, err := tenant.CommitBlobUpload(ctx, store.VerifiedUploadCommit{TicketID: reserved.ID, Authority: request.Intent.Authority, Verified: reserved.Blob, ReplicaID: "fixture"}); err != nil {
		return err
	}
	return objects.DeleteObject(ctx, pending)
}
