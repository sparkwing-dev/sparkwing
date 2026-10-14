package blob_test

import (
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestReferenceEdges(t *testing.T) {
	i := identity()
	a := blob.Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Attempt: i.Attempt}
	r := blob.Receipt{Identity: i, Digest: digest("bytes")}
	request := blob.EdgeRequest{TargetKind: blob.KindManifest, Authority: a, Digest: r.Digest, OwnReceipt: runspec.Optional[blob.Receipt]{Set: true, Value: r}}
	if err := blob.AuthorizeEdge(request); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"team", "run", "job", "attempt", "generation", "principal", "capability", "digest"} {
		t.Run(field, func(t *testing.T) {
			bad := request
			switch field {
			case "team":
				bad.OwnReceipt.Value.Identity.Attempt.Value.Team = "other"
			case "run":
				bad.OwnReceipt.Value.Identity.Attempt.Value.RunID = "other"
			case "job":
				bad.OwnReceipt.Value.Identity.Attempt.Value.NodeID = "other"
			case "attempt":
				bad.OwnReceipt.Value.Identity.Attempt.Value.Attempt++
			case "generation":
				bad.OwnReceipt.Value.Identity.Attempt.Value.Generation++
			case "principal":
				bad.OwnReceipt.Value.Identity.Principal = "other"
			case "capability":
				bad.OwnReceipt.Value.Identity.CapabilityRef = "other"
			case "digest":
				bad.OwnReceipt.Value.Digest = digest("other")
			}
			if err := blob.AuthorizeEdge(bad); !errors.Is(err, blob.ErrDigestNotReadable) {
				t.Fatalf("unauthorized edge: %v", err)
			}
		})
	}
	request.OwnReceipt.Set = false
	for _, kind := range []blob.EdgeKind{blob.EdgeDependency, blob.EdgeDeclaredReference, blob.EdgeMemoHit, blob.EdgeMemoScope, blob.EdgeOwnUpload} {
		request.ReadSet = blob.ReadSet{Reader: i.Attempt.Value, Edges: []blob.ReadEdge{{Kind: kind, Reader: i.Attempt.Value, Digest: r.Digest}}}
		if err := blob.AuthorizeEdge(request); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	request.ReadSet.Reader.NodeID = "unrelated"
	if err := blob.AuthorizeEdge(request); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("foreign read set accepted")
	}
}

func TestNamespacesAndKeys(t *testing.T) {
	key := blob.MemoKey{Repo: "gh:123", Ref: blob.RefScope{Ref: "main", Verified: true}, Pipeline: "build", Job: "compile", Hash: runspec.Hash(digest("key"))}
	trusted, err := blob.MemoNamespace(key)
	if err != nil {
		t.Fatal(err)
	}
	key.Ref.Verified = false
	manual, err := blob.MemoNamespace(key)
	if err != nil || manual == trusted {
		t.Fatalf("manual/trusted collision: %s, %v", manual, err)
	}
	key.Ref = blob.RefScope{Ref: "manual:main", Verified: true}
	literal, err := blob.MemoNamespace(key)
	if err != nil || literal == manual {
		t.Fatalf("manual prefix collision: %s, %v", literal, err)
	}
	key.Pipeline = "build/compile"
	key.Job = "job"
	x, _ := blob.MemoNamespace(key)
	key.Pipeline = "build"
	key.Job = "compile/job"
	y, _ := blob.MemoNamespace(key)
	if x == y {
		t.Fatal("namespace separator collision")
	}
	for _, team := range []string{"", ".", "..", "a/b", "a\\b", "a%2fb", "a\x00b"} {
		if _, err := blob.ObjectKey(team, digest("a")); err == nil {
			t.Fatalf("invalid team %q accepted", team)
		}
	}
	got, err := blob.ObjectKey("team", digest("a"))
	if err != nil || got != "teams/team/cas/"+string(digest("a")) {
		t.Fatalf("ObjectKey=%q %v", got, err)
	}
}

func TestBinaryProvenance(t *testing.T) {
	record := blob.Record{Key: blob.RecordKey{Team: "team", Kind: blob.KindBinary}, Digest: digest("bin"), Provenance: blob.ProvenanceCloud}
	exec := blob.BinaryExecution{Record: record, Digest: record.Digest, Trust: blob.BuildTrust{Team: "team"}}
	if err := blob.CheckBinaryExecution(exec); err != nil {
		t.Fatal(err)
	}
	exec.Record.Provenance = blob.ProvenanceLocal
	if err := blob.CheckBinaryExecution(exec); err == nil {
		t.Fatal("local binary trusted without opt-in")
	}
	exec.Trust.TrustLocalBuilds = true
	if err := blob.CheckBinaryExecution(exec); err != nil {
		t.Fatal(err)
	}
	exec.Digest = digest("changed")
	if err := blob.CheckBinaryExecution(exec); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("changed binary: %v", err)
	}
}

func TestResolveWriteKeyUsesSealedRef(t *testing.T) {
	i := identity()
	a := blob.Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Attempt: i.Attempt, Repo: "gh:123", Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "main"}}}
	intent := blob.WriteScopeIntent{Kind: blob.KindMemo, Pipeline: "build", Job: "job", Key: string(digest("input"))}
	manual, err := blob.ResolveWriteKey(a, intent)
	if err != nil {
		t.Fatal(err)
	}
	if manual.Team != "team" || manual.Scope.Repo != "gh:123" || manual.Scope.Ref.Verified {
		t.Fatalf("write key=%+v", manual)
	}
	a.Ref.Scope.Verified = true
	if _, err := blob.ResolveWriteKey(a, intent); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("typed main gained verified authority")
	}
	a.Ref.AppDeliveryKey = "delivery"
	a.Ref.SHA = "sealed-sha"
	verified, err := blob.ResolveWriteKey(a, intent)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := blob.RecordNamespace(manual)
	y, _ := blob.RecordNamespace(verified)
	if x == y {
		t.Fatal("manual write can overwrite verified write")
	}
	intent.Job = "sibling"
	if _, err := blob.ResolveWriteKey(a, intent); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatal("job selected sibling namespace")
	}
}

func TestLogSubjectIdentity(t *testing.T) {
	a := identity().Attempt.Value
	valid := []blob.LogSubject{
		{Kind: blob.LogJobAttempt, Team: a.Team, RunID: a.RunID, Attempt: &a},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 5, Source: &blob.LogSource{NodeID: "source", Generation: 2}}},
	}
	for _, subject := range valid {
		if err := subject.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	badAttempt := a
	badAttempt.Attempt = 0
	invalid := []blob.LogSubject{
		{},
		{Kind: blob.LogJobAttempt, Team: "foreign", RunID: a.RunID, Attempt: &a},
		{Kind: blob.LogJobAttempt, Team: a.Team, RunID: a.RunID, Attempt: &badAttempt},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Attempt: &a, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 1}},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 0}},
		{Kind: blob.LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: 1, Source: &blob.LogSource{NodeID: "source", Generation: 1}}},
	}
	for _, subject := range invalid {
		if err := subject.Validate(); err == nil {
			t.Fatalf("accepted %+v", subject)
		}
	}
}
