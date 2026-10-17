package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobSourceSetupReadRemainsFencedWithoutAnAttempt(t *testing.T) {
	for _, previousAttempts := range []int64{0, 2} {
		for _, mutation := range []string{"none", "foreign run", "other digest", "other kind", "stale generation", "other holder", "cancelled", "expired"} {
			t.Run(fmt.Sprintf("%d/%s", previousAttempts, mutation), func(t *testing.T) {
				checkBlobSourceSetup(t, previousAttempts, mutation, strings.Repeat("b", 64))
			})
		}
	}
}

func FuzzBlobSourceSetupReadRemainsFenced(f *testing.F) {
	for selector := range 8 {
		f.Add(uint8(selector), uint16(0), "first acquisition")
		f.Add(uint8(selector), uint16(2), "retry acquisition")
	}
	f.Fuzz(func(t *testing.T, selector uint8, previousAttempts uint16, seed string) {
		mutations := []string{"none", "foreign run", "other digest", "other kind", "stale generation", "other holder", "cancelled", "expired"}
		digest := sha256.Sum256([]byte(seed))
		checkBlobSourceSetup(t, int64(previousAttempts), mutations[int(selector)%len(mutations)], hex.EncodeToString(digest[:]))
	})
}

func checkBlobSourceSetup(t *testing.T, previousAttempts int64, mutation, sourceDigest string) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	if _, err := records.CommitRecord(t.Context(), blobOutputRequest(intent)); err != nil {
		t.Fatal(err)
	}
	tenant := records.tenant
	rewriteSchemaExec(t, s.db, `DELETE FROM node_attempts WHERE team='a' AND run_id='run'`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET status='pending',started_at=NULL,attempt=? WHERE team='a' AND run_id='run' AND node_id='job'`, previousAttempts)
	seedSourceSetupBundle(t, s, sourceDigest)
	switch mutation {
	case "foreign run":
		rewriteSchemaExec(t, s.db, `UPDATE blob_records SET run_id=NULL WHERE kind='source_bundle'`)
	case "other digest":
		rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE id='run'`, []byte(`{"request":{"git":{"ref":"main"},"source":{"bundle_digest":{"set":true,"value":"different"}}}}`))
	case "other kind":
		rewriteSchemaExec(t, s.db, `UPDATE blob_records SET kind='binary' WHERE kind='source_bundle'`)
	case "stale generation":
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=generation+?`, previousAttempts+1)
	case "other holder":
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET claimed_by=?`, "attacker-"+sourceDigest)
	case "cancelled":
		rewriteSchemaExec(t, s.db, `UPDATE runs SET cancel_requested_at=1 WHERE id='run'`)
	case "expired":
		rewriteSchemaExec(t, s.db, `UPDATE nodes SET lease_expires_at=1`)
	}
	authority := blob.Authority{CapabilityRef: intent.Authority.CapabilityRef}
	read, err := tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: authority, Digests: []blob.Digest{blob.Digest(sourceDigest), intent.Digest}})
	allowed := mutation == "none"
	if allowed && (err != nil || len(read.Present) != 1 || string(read.Present[0]) != sourceDigest) || !allowed && err == nil && len(read.Present) != 0 {
		t.Fatalf("source setup read=%+v %v, allowed=%v", read, err, allowed)
	}
	if _, err := tenant.ResolveBlobAuthority(t.Context(), authority, capability.DataWrite); err == nil {
		t.Fatal("pre-attempt source read granted upload authority")
	}
}

func seedSourceSetupBundle(t *testing.T, s *Store, sourceDigest string) {
	t.Helper()
	seal := runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1"}, Ref: "main"}, Source: runspec.Source{BundleDigest: runspec.Optional[string]{Set: true, Value: sourceDigest}}}}
	rawSeal, err := json.Marshal(seal)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, rawSeal)
	identity := blob.UploadIdentity{Kind: blob.UploadSubmission, Team: "a", RunID: "run", Principal: "requester", CapabilityRef: "member"}
	sourceJSON, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)`, sourceDigest)
	rewriteSchemaExec(t, s.db, `INSERT INTO blob_records(team,kind,scope,name,digest,run_id,repo_key,ref_scope,uploader_principal,provenance,source_identity_json,source_kind,source_run_id,verified_ref,verified_default_branch,ref_facts_json,pipeline,created_at) VALUES ('a','source_bundle','source','source',?,'run','gh:1','manual:main','requester','local',?,'submission','run',0,0,?,'ci',1)`, sourceDigest, sourceJSON, []byte(`{"scope":{"ref":"main"}}`))
}
