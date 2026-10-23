package store

import (
	"errors"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestBlobRecordsReplayKeepsEquivalentExpiry(t *testing.T) {
	_, records, intent := blobRecordFixture(t)
	request := blobOutputRequest(intent)
	expiry := records.tenant.s.now().Add(time.Hour)
	request.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: expiry.In(time.FixedZone("client", 3600))}
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.ExpiresAt.Value = expiry.In(time.FixedZone("replay", -3600))
	if _, err := records.CommitRecord(t.Context(), request); err != nil {
		t.Fatalf("same expiry instant refused: %v", err)
	}
}

func TestBlobRecordsReplayNormalizesOptionalMetadata(t *testing.T) {
	for _, kind := range []blob.Kind{blob.KindOutput, blob.KindManifest} {
		t.Run(string(kind), func(t *testing.T) {
			property := func(offset int32, nanos uint32, expires bool, unused string) bool {
				s, records, intent := blobRecordFixture(t)
				path := blobAccountingDiskStore(t, s)
				records.manifests = fakeBlobManifests{intent.Digest: nil}
				request := blobOutputRequest(intent)
				request.Key.Kind = kind
				expiry := s.now().Add(time.Hour).Add(time.Duration(nanos))
				request.ExpiresAt = runspec.Optional[time.Time]{Set: expires, Value: expiry.In(time.FixedZone("client", int(offset%86400)))}
				request.ManifestDigest = runspec.Optional[blob.Digest]{Value: blob.Digest(unused)}
				first, err := records.CommitRecord(t.Context(), request)
				if err != nil {
					t.Logf("first commit: %v", err)
					return false
				}
				if expires {
					if first.ExpiresAt.Value.Location() != time.UTC || !first.ExpiresAt.Value.Equal(expiry) {
						t.Logf("noncanonical expiry: %+v", first.ExpiresAt)
						return false
					}
				} else if first.ExpiresAt != (runspec.Optional[time.Time]{}) {
					t.Logf("absent expiry retained a value: %+v", first.ExpiresAt)
					return false
				}
				if first.ManifestDigest != (runspec.Optional[blob.Digest]{}) {
					t.Logf("absent manifest retained a value: %+v", first.ManifestDigest)
					return false
				}
				if err := s.db.Close(); err != nil {
					t.Fatal(err)
				}
				s.db = blobAccountingDB(t, path)
				request.ExpiresAt = runspec.Optional[time.Time]{Set: expires}
				if expires {
					request.ExpiresAt.Value = expiry.UTC()
				}
				request.ManifestDigest = runspec.Optional[blob.Digest]{}
				replayed, err := records.CommitRecord(t.Context(), request)
				if err != nil || replayed != first {
					t.Logf("replay=%+v first=%+v: %v", replayed, first, err)
					return false
				}
				request.ExpiresAt = runspec.Optional[time.Time]{Set: true, Value: expiry.Add(time.Nanosecond)}
				_, err = records.CommitRecord(t.Context(), request)
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != apierr.Conflict {
					t.Logf("changed expiry: %v", err)
					return false
				}
				got, err := records.LookupRecord(t.Context(), blob.RecordLookup{Authority: intent.Authority, Key: request.Key})
				if err != nil || !got.Found || got.Record != first {
					t.Logf("record changed after refusal: %+v: %v", got, err)
					return false
				}
				rewriteSchemaCount(t, s.db, "blob_records", 1)
				return true
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 48, Rand: rand.New(rand.NewSource(625019))}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
