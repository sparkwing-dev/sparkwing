package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDataStorageAttachesVerifiedResultRecords(t *testing.T) {
	property := func(payload [16]byte) bool {
		engine, tenant := engineRuntimeStore(t)
		authorized := engineRuntimeClaim(t, engine, capability.UnitNode)
		started, err := engine.TransitionJob(t.Context(), authorized, controllerwire.JobTransitionRequest{To: "running", Cause: "start"})
		if err != nil {
			t.Log(err)
			return false
		}
		dir := t.TempDir()
		objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
		if err != nil {
			t.Log(err)
			return false
		}
		defer func() {
			if err := objects.Close(); err != nil {
				t.Error(err)
			}
		}()
		c := authorized.Principal.Capability
		identity := blob.UploadIdentity{Kind: blob.UploadJob, Team: c.Team, RunID: c.Binding.RunID, Principal: blob.Principal(c.Subject.ID), CapabilityRef: c.Prefix, Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: c.Team, RunID: c.Binding.RunID, NodeID: c.Binding.NodeID, Attempt: started.Attempt, Generation: started.Generation}}}
		authority := blob.Authority{CapabilityRef: c.Prefix, Attempt: identity.Attempt}
		output := dataResultUpload(t, tenant, objects, authority, identity, "output", payload[:])
		manifest := blob.Manifest{Entries: []blob.ManifestEntry{{Path: "result", Digest: output.Digest, Size: output.Size, Mode: 0o640}}}
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Log(err)
			return false
		}
		stored := dataResultUpload(t, tenant, objects, authority, identity, "manifest", raw)
		records := tenant.BlobRecords(storage.BlobManifests{Objects: objects})
		outputKey := blob.ResultRecordKey(identity.Attempt.Value, "gh:1", blob.RefScope{}, "ci", blob.KindOutput)
		manifestKey := blob.ResultRecordKey(identity.Attempt.Value, "gh:1", blob.RefScope{}, "ci", blob.KindManifest)
		for _, request := range []blob.CommitRecordRequest{
			{Authority: authority, Source: identity, Key: outputKey, Digest: output.Digest},
			{Authority: authority, Source: identity, Key: manifestKey, Digest: stored.Digest, Members: []blob.Member{{Team: c.Team, ManifestDigest: stored.Digest, Digest: output.Digest, Path: "result", Size: output.Size, Mode: 0o640}}},
		} {
			if _, err := records.CommitRecord(t.Context(), request); err != nil {
				t.Log(err)
				return false
			}
		}
		New(engine.Store, nil).WithDataObjects(objects)
		key, err := blob.ObjectKey(c.Team, stored.Digest)
		if err != nil {
			t.Log(err)
			return false
		}
		path := filepath.Join(dir, filepath.FromSlash(key))
		if err := os.WriteFile(path, append(bytes.Clone(raw), 'x'), 0o600); err != nil {
			t.Log(err)
			return false
		}
		request := controllerwire.JobTransitionRequest{To: "success", Cause: "report", Result: &controllerwire.JobResult{Outcome: "success", Output: &outputKey, Manifest: &manifestKey}}
		if _, err := engine.TransitionJob(t.Context(), authorized, request); !errors.Is(err, blob.ErrIntegrity) {
			t.Logf("corrupt result: %v", err)
			return false
		}
		row, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil || row.Status != store.StatusRunning {
			t.Logf("refused result: %+v: %v", row, err)
			return false
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Log(err)
			return false
		}
		for i := range 2 {
			applied, err := engine.TransitionJob(t.Context(), authorized, request)
			if err != nil || applied.Replayed != (i == 1) {
				t.Logf("verified result %d: %+v: %v", i, applied, err)
				return false
			}
		}
		row, err = tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
		return err == nil && row.Status == store.StatusDone && row.Outcome == store.NodeSuccess
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12, Rand: rand.New(rand.NewSource(71))}); err != nil {
		t.Fatal(err)
	}
}

func dataResultUpload(t *testing.T, tenant *store.Tenant, objects *blob.Filesystem, authority blob.Authority, identity blob.UploadIdentity, id string, body []byte) blob.Blob {
	t.Helper()
	digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(body)))
	reservation, err := tenant.ReserveBlobUpload(t.Context(), store.UploadReservationRequest{ID: id, Intent: blob.UploadIntent{Authority: authority, Identity: identity, Digest: digest, Size: int64(len(body))}, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: reservation.StagingKey, Size: reservation.Blob.Size, SHA256: string(reservation.Blob.Digest), ExpiresAt: reservation.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	objects.ServeHTTP(response, httptest.NewRequest(http.MethodPut, signed.URL, bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("signed PUT: %d %s", response.Code, response.Body)
	}
	if err := objects.CommitObject(t.Context(), reservation.StagingKey, reservation.Blob); err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.CommitBlobUpload(t.Context(), store.VerifiedUploadCommit{Authority: authority, TicketID: id, Verified: reservation.Blob, ReplicaID: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := objects.DeleteObject(t.Context(), reservation.StagingKey); err != nil {
		t.Fatal(err)
	}
	return reservation.Blob
}
