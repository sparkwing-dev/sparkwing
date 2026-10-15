package store

import (
	"fmt"
	"math/rand"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestBlobAuthorityRejectsForgedProjectionProperty(t *testing.T) {
	s, records, intent := blobRecordFixture(t)
	tenant := records.tenant
	selector := blob.Authority{CapabilityRef: intent.Authority.CapabilityRef}
	actual, err := tenant.ResolveBlobAuthority(t.Context(), selector, capability.DataRead)
	if err != nil || !actual.Upload.Set || actual.Upload.Value != intent.Identity || actual.Attempt != intent.Identity.Attempt || actual.Ref.Scope.Ref != "main" || actual.Ref.Scope.Verified {
		t.Fatalf("persisted authority: %+v: %v", actual, err)
	}
	property := func(value uint32, field uint8) bool {
		input := actual
		other := fmt.Sprintf("other-%d", value)
		switch field % 8 {
		case 0:
			input.Principal = blob.Principal(other)
		case 1:
			input.Repo += runspec.RepoKey("/" + other)
		case 2:
			input.Ref.Scope.Ref += other
		case 3:
			input.Ref.Scope.Verified = true
		case 4:
			input.Attempt.Value.Attempt += int64(value) + 1
		case 5:
			input.Upload.Value.Principal = blob.Principal(other)
		case 6:
			input.Upload.Value.Unit.Set = true
		case 7:
			input.CapabilityRef = other
		}
		got, err := tenant.ResolveBlobAuthority(t.Context(), input, capability.DataRead)
		return err != nil && got == (blob.Authority{})
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(751))}); err != nil {
		t.Fatal(err)
	}
	foreign := &Tenant{s: s, team: "b"}
	if got, err := foreign.ResolveBlobAuthority(t.Context(), selector, capability.DataRead); err == nil || got != (blob.Authority{}) {
		t.Fatalf("foreign authority: %+v: %v", got, err)
	}
	if got, err := tenant.ResolveBlobAuthority(t.Context(), selector, capability.LogTail); err == nil || got != (blob.Authority{}) {
		t.Fatalf("non-data grant: %+v: %v", got, err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET revoked_at=? WHERE prefix=?", s.now().UnixNano(), selector.CapabilityRef)
	if got, err := tenant.ResolveBlobAuthority(t.Context(), actual, capability.DataRead); err == nil || got != (blob.Authority{}) {
		t.Fatalf("revoked authority: %+v: %v", got, err)
	}
}
