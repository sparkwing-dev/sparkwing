package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// safety: the fixture run becomes fixtureWriter's asserted run on ownRef, granted readRefs after its own scope.
func writerDefaultFixture(t *testing.T, ownRef string, readRefs ...string) (*Store, *blobRecords, blob.UploadIntent) {
	t.Helper()
	s, records, intent := blobRecordFixture(t)
	body := sealWith(t, runspec.SealedRequest{Version: 1, Request: runspec.Request{Git: runspec.Git{Ref: ownRef}}}, assertedProvenance(t, "ci", ownRef, ""))
	rewriteSchemaExec(t, s.db, "UPDATE runs SET request_json=? WHERE team='a' AND id='run'", body)
	grantFixtureReadRefs(t, s, intent.Authority.CapabilityRef, assertedRef(ownRef).CapabilityScope(), readRefs...)
	intent.Authority.Ref = blob.RefFacts{Scope: assertedRef(ownRef)}
	return s, records, intent
}

func grantFixtureReadRefs(t *testing.T, s *Store, prefix, write string, extra ...string) {
	t.Helper()
	var body []byte
	if err := s.db.QueryRow("SELECT authority_json FROM capabilities WHERE prefix=?", prefix).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var record CapabilityRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	record.Value.Data.WriteRef = write
	record.Value.Data.ReadRefs = append([]string{write}, extra...)
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, "UPDATE capabilities SET authority_json=?,ref_scope=? WHERE prefix=?", body, write, prefix)
}

func writerDefaultLookups(t *testing.T, records *blobRecords, intent blob.UploadIntent, own blob.RefScope) (blob.MemoHit, blob.DepcacheHit) {
	t.Helper()
	memo, err := records.LookupMemo(t.Context(), blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: own, Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}})
	if err != nil {
		t.Fatal(err)
	}
	dep, err := records.LookupDepcache(t.Context(), blob.DepcacheLookup{Authority: intent.Authority, Key: blob.DepcacheKey{Repo: "gh:1", Ref: own, Name: "modules", Key: fixtureMemoHash}})
	if err != nil {
		t.Fatal(err)
	}
	return memo, dep
}

// safety: these bytes have no upload receipt, so only a read edge can make them readable.
func freshFixtureBlob(t *testing.T, s *Store, intent blob.UploadIntent, fill string) blob.UploadIntent {
	t.Helper()
	intent.Digest = blob.Digest(strings.Repeat(fill, 64))
	rewriteSchemaExec(t, s.db, "INSERT INTO blobs(team,digest,size,created_at) VALUES ('a',?,3,1)", intent.Digest)
	return intent
}

func plantWriterRecords(t *testing.T, s *Store, intent blob.UploadIntent, scope blob.RefScope) {
	t.Helper()
	for _, kind := range []blob.Kind{blob.KindMemo, blob.KindDepcache} {
		plantBlobRecordAt(t, s, intent, kind, scope, blob.RefFacts{Scope: scope}, false)
	}
}

func TestAssertedBranchReadsItsWriterDefaultBranchAfterItsOwn(t *testing.T) {
	for _, own := range []string{"feat", ""} {
		t.Run("own="+own, func(t *testing.T) {
			main := assertedRef("main")
			s, records, intent := writerDefaultFixture(t, own, main.CapabilityScope())
			stored := freshFixtureBlob(t, s, intent, "b")
			plantWriterRecords(t, s, stored, main)
			memo, dep := writerDefaultLookups(t, records, intent, assertedRef(own))
			if !memo.Found || len(memo.ReadSet.Edges) == 0 || memo.ReadSet.Edges[0].Record.Scope.Ref != main {
				t.Fatalf("memo from the writer's default branch=%+v", memo)
			}
			if !dep.Found || dep.Source != blob.RestoreWriterDefault || dep.Record.Key.Scope.Ref != main || dep.Record.RefFacts.Scope.Verified {
				t.Fatalf("depcache from the writer's default branch=%+v", dep)
			}
			presence, err := records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: intent.Authority, Digests: []blob.Digest{stored.Digest}})
			if err != nil || len(presence.Present) != 1 {
				t.Fatalf("writer-default bytes unreadable: %+v %v", presence, err)
			}

			ownDigest := freshFixtureBlob(t, s, intent, "c")
			plantWriterRecords(t, s, ownDigest, assertedRef(own))
			memo, dep = writerDefaultLookups(t, records, intent, assertedRef(own))
			if !memo.Found || memo.Output.Value != ownDigest.Digest {
				t.Fatalf("own memo lost priority: %+v", memo)
			}
			if !dep.Found || dep.Source != blob.RestoreOwnRef || dep.Record.Digest != ownDigest.Digest {
				t.Fatalf("own depcache lost priority: %+v", dep)
			}
		})
	}
}

func TestAssertedBranchWritesStayInItsOwnScope(t *testing.T) {
	main := assertedRef("main")
	s, records, intent := writerDefaultFixture(t, "feat", main.CapabilityScope())
	plantWriterRecords(t, s, freshFixtureBlob(t, s, intent, "b"), main)
	request := blob.MemoLookup{Authority: intent.Authority, Key: blob.MemoKey{Repo: "gh:1", Ref: assertedRef("feat"), Pipeline: "ci", Job: "job", Hash: fixtureMemoHash}}
	copied, err := records.CopyMemo(t.Context(), blob.CopyMemoRequest{Lookup: request, Destination: intent.Identity.Attempt.Value})
	if err != nil || !copied.Found {
		t.Fatalf("copy from the writer's default branch=%+v: %v", copied, err)
	}
	for _, edge := range copied.ReadSet.Edges {
		if edge.Kind == blob.EdgeMemoHit && edge.Record.Scope.Ref != assertedRef("feat") {
			t.Fatalf("memo copy landed in %+v", edge.Record.Scope.Ref)
		}
	}
	write := blobOutputRequest(intent)
	write.Key.Scope.Ref = main
	if _, err := records.CommitRecord(t.Context(), write); err == nil {
		t.Fatal("a branch run wrote its writer's default-branch scope")
	}
}

// A run reads its writer's default branch only through the grant, and only for
// its own writer: another writer's scope stays unreadable even when a grant names it.
func TestAssertedBranchNeverReadsAnotherWritersDefaultBranch(t *testing.T) {
	other := blob.RefScope{Ref: "main", Writer: string(runspec.PrincipalWriter("account", "other"))}
	for _, test := range []struct {
		name  string
		grant []string
	}{
		{"no grant", nil},
		{"granted", []string{other.CapabilityScope()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, records, intent := writerDefaultFixture(t, "feat", test.grant...)
			stored := freshFixtureBlob(t, s, intent, "b")
			plantWriterRecords(t, s, stored, other)
			plantWriterRecords(t, s, stored, assertedRef("main"))
			memo, dep := writerDefaultLookups(t, records, intent, assertedRef("feat"))
			if memo.Found || dep.Found {
				t.Fatalf("read outside the grant's own writer: memo=%+v dep=%+v", memo, dep)
			}
			presence, err := records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: intent.Authority, Digests: []blob.Digest{stored.Digest}})
			if err != nil || len(presence.Present) != 0 {
				t.Fatalf("bytes readable outside the run's writer: %+v %v", presence, err)
			}
		})
	}
}

// A verified run never reads an asserted writer's scope, even when its grant names
// one, so no asserted record can reach verified work.
func TestVerifiedRunNeverReadsAnAssertedWriterScope(t *testing.T) {
	for _, scope := range []blob.RefScope{assertedRef("refs/heads/main"), assertedRef("refs/heads/feature")} {
		t.Run(scope.CapabilityScope(), func(t *testing.T) {
			s, records, intent := blobRestoreFixture(t)
			grantFixtureReadRefs(t, s, intent.Authority.CapabilityRef, "refs/heads/feature", "refs/heads/release", "refs/heads/main", scope.CapabilityScope())
			stored := freshFixtureBlob(t, s, intent, "b")
			plantWriterRecords(t, s, stored, scope)
			memo, dep := writerDefaultLookups(t, records, intent, blob.RefScope{Ref: "refs/heads/feature", Verified: true})
			if memo.Found || dep.Found {
				t.Fatalf("verified run read an asserted scope: memo=%+v dep=%+v", memo, dep)
			}
			presence, err := records.tenant.ReadableBlobs(t.Context(), blob.HasManyRequest{Authority: intent.Authority, Digests: []blob.Digest{stored.Digest}})
			if err != nil || len(presence.Present) != 0 {
				t.Fatalf("verified run can read asserted bytes: %+v %v", presence, err)
			}
			write := blobOutputRequest(stored)
			write.Key.Scope.Ref = blob.RefScope{Ref: "refs/heads/feature", Verified: true}
			if _, err := records.CommitRecord(t.Context(), write); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("verified run referenced asserted bytes: %v", err)
			}
		})
	}
}

// Only an asserted run reads a writer's default branch, and only its own writer's,
// host partition included.
func TestWriterDefaultScopeKeepsTrustAndWriter(t *testing.T) {
	app := runspec.AppFacts{Repository: "acme/repo", Event: "pull_request", RepositoryID: 1, Ref: "refs/heads/feature", SHA: fixtureFeatureSHA, DeliveryKey: "feature-delivery", DefaultBranch: "main", VerifiedAt: time.Unix(1, 0).UTC()}
	verified, err := runspec.AppProvenance(app, runspec.Optional[runspec.PullRequest]{}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	actions, err := runspec.ActionsProvenance(1, "acme/repo", "refs/heads/feature", fixtureFeatureSHA, "ci")
	if err != nil {
		t.Fatal(err)
	}
	asserted := assertedProvenance(t, "ci", "feat", "")
	host := hostScope(blob.ScopeFor(asserted), "host-1")
	for _, test := range []struct {
		name string
		p    runspec.Provenance
		own  blob.RefScope
		want string
	}{
		{"verified", verified, blob.ScopeFor(verified), ""},
		{"verified on a persistent host", verified, hostScope(blob.ScopeFor(verified), "host-1"), ""},
		{"actions", actions, blob.ScopeFor(actions), ""},
		{"asserted", asserted, blob.ScopeFor(asserted), blob.RefScope{Ref: "main", Writer: string(fixtureWriter)}.CapabilityScope()},
		{"asserted on a persistent host", asserted, host, blob.RefScope{Ref: "main", Writer: host.Writer}.CapabilityScope()},
	} {
		if got := writerDefaultScope(test.p, test.own, "main"); got != test.want {
			t.Errorf("%s: writer default %q, want %q", test.name, got, test.want)
		}
	}
}
