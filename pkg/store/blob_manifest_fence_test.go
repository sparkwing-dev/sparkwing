package store

import (
	"context"
	"encoding/json"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobRecordsManifestIOCannotOutliveAuthority(t *testing.T) {
	for _, operation := range []string{"commit", "record", "memo", "depcache", "copy"} {
		t.Run(operation, func(t *testing.T) {
			s, records, intent := blobRecordFixture(t)
			base := s.now()
			manifest := intent
			manifest.Digest = blob.Digest("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			if _, err := commitTestBlob(t, records.tenant, manifest); err != nil {
				t.Fatal(err)
			}
			members := []blob.Member{{Team: "a", ManifestDigest: manifest.Digest, Digest: intent.Digest, Path: "output", Mode: 0o644, Size: intent.Size}}
			records.manifests = fakeBlobManifests{manifest.Digest: members}
			memo := blobOutputRequest(intent)
			memo.Key.Kind, memo.Key.Name, memo.Key.Scope.Key = blob.KindMemo, "output", "input"
			memo.ManifestDigest = runspec.Optional[blob.Digest]{Set: true, Value: manifest.Digest}
			memo.Members = members
			if _, err := records.CommitRecord(t.Context(), memo); err != nil {
				t.Fatal(err)
			}
			depcache := memo
			depcache.Key = blob.RecordKey{Team: "a", Kind: blob.KindDepcache, Scope: blob.Scope{Repo: "gh:1", Key: "v1"}, Name: "modules"}
			if _, err := records.CommitRecord(t.Context(), depcache); err != nil {
				t.Fatal(err)
			}
			lookup := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Pipeline: "ci", Job: "job", Hash: "input"}}
			property := func(remaining, stalled, fence uint8) bool {
				now := base
				s.clock = func() time.Time { return now }
				expiry := base.Add(time.Duration(1+int(remaining)) * time.Nanosecond)
				lease := base.Add(time.Hour)
				if fence%3 == 0 {
					lease = expiry
				}
				rewriteSchemaExec(t, s.db, "UPDATE nodes SET lease_expires_at=? WHERE team='a' AND run_id='run' AND node_id='job'", lease.UnixNano())
				for i, prefix := range []string{intent.Authority.CapabilityRef, "machine"} {
					var body []byte
					if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix=?", prefix).Scan(&body); err != nil {
						t.Fatal(err)
					}
					var record CapabilityRecord
					if err := json.Unmarshal(body, &record); err != nil {
						t.Fatal(err)
					}
					record.Value.ExpiresAt = nil
					if int(fence%3) == i+1 {
						record.Value.ExpiresAt = &expiry
					}
					body, err := json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,expires_at=? WHERE prefix=?", body, optionalTime(record.Value.ExpiresAt), prefix)
				}
				reads := 0
				records.manifests = blobManifestFunc(func(context.Context, Team, blob.Digest) ([]blob.Member, error) {
					reads++
					now = expiry.Add(time.Duration(stalled) * time.Nanosecond)
					return members, nil
				})
				var err error
				exposed := false
				switch operation {
				case "commit":
					request := blobOutputRequest(intent)
					request.Key.Kind, request.Digest, request.Members = blob.KindManifest, manifest.Digest, members
					_, err = records.CommitRecord(t.Context(), request)
				case "record":
					var hit blob.RecordLookupResult
					hit, err = records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: memo.Key})
					exposed = hit.Found || len(hit.Members) != 0 || len(hit.ReadSet.Edges) != 0
				case "memo":
					var hit blob.MemoHit
					hit, err = records.LookupMemo(t.Context(), lookup)
					exposed = hit.Found || len(hit.Members) != 0 || len(hit.ReadSet.Edges) != 0
				case "depcache":
					var hit blob.DepcacheHit
					hit, err = records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Name: "modules", Key: "v1"}})
					exposed = hit.Found || len(hit.ReadSet.Edges) != 0
				case "copy":
					var hit blob.MemoHit
					hit, err = records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: lookup, Destination: intent.Identity.Attempt.Value})
					exposed = hit.Found || len(hit.Members) != 0 || len(hit.ReadSet.Edges) != 0
				}
				if reads != 1 || err == nil || exposed {
					return false
				}
				var count int
				if err := s.db.QueryRow("SELECT COUNT(*) FROM blob_records WHERE kind IN ('output','manifest')").Scan(&count); err != nil {
					t.Fatal(err)
				}
				return count == 0
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 144, Rand: rand.New(rand.NewSource(29))}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
