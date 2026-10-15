package store

import (
	"encoding/json"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type expiringBlobNamespace struct {
	fakeBlobRules
	expire func()
}

func (r expiringBlobNamespace) RecordNamespace(key blob.RecordKey) (string, error) {
	if key.Kind == blob.KindOutput {
		r.expire()
	}
	return r.fakeBlobRules.RecordNamespace(key)
}

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
						memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", "input"
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
					crossed := false
					records.rules = expiringBlobNamespace{expire: func() {
						crossed = true
						now = expiry.Add(time.Duration(elapsed) * time.Nanosecond)
						if !expired {
							now = expiry.Add(-time.Nanosecond)
						}
					}}
					var err error
					exposed := false
					if operation == "commit" {
						var record blob.Record
						record, err = records.CommitRecord(t.Context(), blobOutputRequest(intent))
						exposed = record.Digest != ""
					} else {
						var hit blob.MemoHit
						hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{
							Lookup:      blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Pipeline: "ci", Job: "job", Hash: "input"}},
							Destination: intent.Identity.Attempt.Value,
						})
						exposed = hit.Found || len(hit.ReadSet.Edges) != 0
					}
					var count int
					if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE kind IN ('output','manifest')").Scan(&count); err != nil {
						t.Fatal(err)
					}
					valid := crossed && err != nil && !exposed && count == 0
					if !expired {
						valid = crossed && err == nil && exposed && count == 1
					}
					if !valid {
						t.Logf("crossed=%v exposed=%v records=%d error=%v", crossed, exposed, count, err)
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
