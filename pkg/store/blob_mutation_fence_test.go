package store

import (
	"encoding/json"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobRecordsMutationCannotOutliveAuthority(t *testing.T) {
	for _, operation := range []string{"commit", "copy"} {
		for _, fence := range []string{"lease", "leaf", "ancestor"} {
			t.Run(operation+"/"+fence, func(t *testing.T) {
				property := func(remaining, elapsed uint8, expired bool) bool {
					s, records, intent := blobRecordFixture(t)
					now := s.now()
					s.clock = func() time.Time { return now }
					expiry := now.Add(time.Duration(int(remaining)+1) * time.Microsecond)
					if operation == "copy" {
						memo := blobOutputRequest(intent)
						memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", fixtureMemoHash
						if _, err := records.CommitRecord(t.Context(), memo); err != nil {
							t.Fatal(err)
						}
					}
					if fence == "lease" {
						rewriteSchemaExec(t, s.db, "UPDATE nodes SET lease_expires_at=? WHERE team='a' AND run_id='run' AND node_id='job'", expiry.UnixNano())
					} else {
						prefix := intent.Authority.CapabilityRef
						if fence == "ancestor" {
							prefix = "machine"
						}
						var body []byte
						if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix=?", prefix).Scan(&body); err != nil {
							t.Fatal(err)
						}
						var record CapabilityRecord
						if err := json.Unmarshal(body, &record); err != nil {
							t.Fatal(err)
						}
						record.Value.ExpiresAt = &expiry
						body, err := json.Marshal(record)
						if err != nil {
							t.Fatal(err)
						}
						rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,expires_at=? WHERE prefix=?", body, expiry.UnixNano(), prefix)
					}
					tx, err := s.beginTx(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer rollbackOrLog(tx)
					authority, err := resolveBlobAuthorityTx(t.Context(), tx, records.tenant.team, intent.Authority, capability.DataWrite)
					if err != nil {
						t.Fatal(err)
					}
					request := blobOutputRequest(intent)
					record := blob.Record{Key: request.Key, Digest: request.Digest, Source: intent.Identity, Uploader: "machine", Provenance: blob.ProvenanceLocal, RefFacts: authority.authority.Ref}
					if operation == "copy" {
						var original blob.Record
						hit, err := records.lookupMemoTx(t.Context(), tx, authority, blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: blob.RefScope{Ref: "main"}, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}, &original)
						if err != nil || !hit.Found {
							t.Fatalf("prepare copy: %+v, %v", hit, err)
						}
						record = original
						record.Key, record.ExpiresAt = request.Key, runspec.Optional[time.Time]{}
					}
					scope, err := blob.RecordNamespace(record.Key)
					if err != nil {
						t.Fatal(err)
					}
					now = expiry.Add(time.Duration(elapsed) * time.Nanosecond)
					if !expired {
						now = expiry.Add(-time.Nanosecond)
					}
					batch, err := records.mutateRecordsTx(t.Context(), tx, authority, []blobRecordMutation{{record: record, scope: scope}}, operation == "copy", nil)
					exposed := len(batch) != 0
					if err == nil {
						err = tx.Commit()
					} else if rollbackErr := tx.Rollback(); rollbackErr != nil {
						t.Fatal(rollbackErr)
					}
					var count int
					if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE kind IN ('output','manifest')").Scan(&count); err != nil {
						t.Fatal(err)
					}
					valid := err != nil && !exposed && count == 0
					if !expired {
						valid = err == nil && exposed && count == 1
					}
					if !valid {
						t.Logf("exposed=%v records=%d error=%v", exposed, count, err)
						return false
					}
					return true
				}
				if !property(1, 0, false) {
					t.Fatal("live control failed")
				}
				if !property(1, 0, true) {
					t.Error("mutation accepted at exact expiry")
				}
				if err := quick.Check(func(remaining, elapsed uint8) bool { return property(remaining, elapsed, true) }, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(625020))}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
