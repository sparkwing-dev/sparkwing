package blob

import (
	"errors"
	"fmt"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestReferenceEdges(t *testing.T) {
	i := identity()
	a := Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Attempt: i.Attempt}
	r := Receipt{Identity: i, Digest: digest("bytes")}
	request := EdgeRequest{TargetKind: KindManifest, Authority: a, Digest: r.Digest, OwnReceipt: runspec.Optional[Receipt]{Set: true, Value: r}}
	if err := AuthorizeEdge(request); err != nil {
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
			if err := AuthorizeEdge(bad); !errors.Is(err, ErrDigestNotReadable) {
				t.Fatalf("unauthorized edge: %v", err)
			}
		})
	}
	request.OwnReceipt.Set = false
	for _, kind := range []EdgeKind{EdgeDependency, EdgeDeclaredReference, EdgeMemoHit, EdgeMemoScope, EdgeOwnUpload} {
		request.ReadSet = ReadSet{Reader: i.Attempt.Value, Edges: []ReadEdge{{Kind: kind, Reader: i.Attempt.Value, Digest: r.Digest}}}
		if err := AuthorizeEdge(request); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	request.ReadSet.Reader.NodeID = "unrelated"
	if err := AuthorizeEdge(request); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("foreign read set accepted")
	}
}

func TestRemoteCacheReferenceEdges(t *testing.T) {
	property := func(seed [16]byte, choice uint8) bool {
		nonce := fmt.Sprintf("%x", seed)
		kinds := []Kind{KindOutput, KindManifest, KindMemo, KindDepcache}
		kind := kinds[int(choice)%len(kinds)]
		request := EdgeRequest{
			TargetKind: kind, Digest: digest(nonce),
			Authority: Authority{CapabilityRef: "reader-" + nonce, Principal: "user-" + Principal(nonce), Repo: "gh:1"},
			ReadSet: ReadSet{Edges: []ReadEdge{{
				Kind: EdgeMemoScope, Digest: digest(nonce),
				Record: RecordKey{Team: "team-" + nonce, Kind: kind, Scope: Scope{Repo: "gh:1", Ref: RefScope{Ref: "main", Verified: true}}},
			}}},
		}
		if AuthorizeEdge(request) != nil {
			return false
		}
		for _, mutate := range []func(*EdgeRequest){
			func(q *EdgeRequest) { q.Authority.Principal = "" },
			func(q *EdgeRequest) { q.Authority.CapabilityRef = "" },
			func(q *EdgeRequest) { q.Authority.Repo = "gh:2" },
			func(q *EdgeRequest) { q.Authority.Upload.Set = true },
			func(q *EdgeRequest) { q.ReadSet.Edges[0].Record.Team = "" },
			func(q *EdgeRequest) { q.ReadSet.Edges[0].Record.Scope.Ref.Verified = false },
			func(q *EdgeRequest) { q.ReadSet.Edges[0].Record.Kind = KindBinary },
			func(q *EdgeRequest) { q.ReadSet.Edges[0].Kind = EdgeDependency },
			func(q *EdgeRequest) { q.ReadSet.Edges[0].Digest = digest("other-" + nonce) },
			func(q *EdgeRequest) { q.ReadSet.Reader = identity().Attempt.Value },
			func(q *EdgeRequest) { q.ReadSet.Edges[0].Reader = identity().Attempt.Value },
			func(q *EdgeRequest) { q.TargetKind = KindBinary },
			func(q *EdgeRequest) { q.TargetKind = KindSourceBundle },
		} {
			changed := request
			changed.ReadSet.Edges = append([]ReadEdge(nil), request.ReadSet.Edges...)
			mutate(&changed)
			if !errors.Is(AuthorizeEdge(changed), ErrDigestNotReadable) {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestNamespacesAndKeys(t *testing.T) {
	key := MemoKey{Repo: "gh:123", Ref: RefScope{Ref: "main", Verified: true}, Pipeline: "build", Job: "compile", Hash: runspec.Hash(digest("key"))}
	trusted, err := MemoNamespace(key)
	if err != nil {
		t.Fatal(err)
	}
	key.Ref = RefScope{Ref: "main", Writer: "account/a"}
	manual, err := MemoNamespace(key)
	if err != nil || manual == trusted {
		t.Fatalf("manual/trusted collision: %s, %v", manual, err)
	}
	key.Ref.Writer = "account/b"
	other, err := MemoNamespace(key)
	if err != nil || other == manual {
		t.Fatalf("one writer's scope reached another's: %s, %v", other, err)
	}
	key.Ref.Writer = ""
	if _, err := MemoNamespace(key); err == nil {
		t.Fatal("an unverified scope with no writer is the shared bucket")
	}
	key.Ref.Writer = "account/a"
	key.Ref = RefScope{Ref: "manual:main", Verified: true}
	literal, err := MemoNamespace(key)
	if err != nil || literal == manual {
		t.Fatalf("manual prefix collision: %s, %v", literal, err)
	}
	key.Pipeline = "build/compile"
	key.Job = "job"
	x, _ := MemoNamespace(key)
	key.Pipeline = "build"
	key.Job = "compile/job"
	y, _ := MemoNamespace(key)
	if x == y {
		t.Fatal("namespace separator collision")
	}
	for _, team := range []string{"", ".", "..", "a/b", "a\\b", "a%2fb", "a\x00b"} {
		if _, err := ObjectKey(team, digest("a")); err == nil {
			t.Fatalf("invalid team %q accepted", team)
		}
	}
	got, err := ObjectKey("team", digest("a"))
	if err != nil || got != "teams/team/cas/"+string(digest("a")) {
		t.Fatalf("ObjectKey=%q %v", got, err)
	}
}

func TestBinaryProvenance(t *testing.T) {
	record := Record{Key: RecordKey{Team: "team", Kind: KindBinary}, Digest: digest("bin"), Provenance: ProvenanceCloud}
	exec := BinaryExecution{Record: record, Digest: record.Digest, Trust: BuildTrust{Team: "team"}}
	if err := CheckBinaryExecution(exec); err != nil {
		t.Fatal(err)
	}
	exec.Record.Provenance = ProvenanceLocal
	if err := CheckBinaryExecution(exec); err == nil {
		t.Fatal("local binary trusted without opt-in")
	}
	exec.Trust.TrustLocalBuilds = true
	if err := CheckBinaryExecution(exec); err != nil {
		t.Fatal(err)
	}
	exec.Digest = digest("changed")
	if err := CheckBinaryExecution(exec); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("changed binary: %v", err)
	}
}

func TestResolveWriteKeyUsesSealedRef(t *testing.T) {
	i := identity()
	a := Authority{Principal: i.Principal, CapabilityRef: i.CapabilityRef, Attempt: i.Attempt, Repo: "gh:123", Ref: RefFacts{Scope: RefScope{Ref: "main", Writer: "account/a"}}}
	intent := WriteScopeIntent{Kind: KindMemo, Pipeline: "build", Job: "job", Key: string(digest("input"))}
	manual, err := ResolveWriteKey(a, intent)
	if err != nil {
		t.Fatal(err)
	}
	if manual.Team != "team" || manual.Scope.Repo != "gh:123" || manual.Scope.Ref.Verified {
		t.Fatalf("write key=%+v", manual)
	}
	a.Ref.Scope = RefScope{Ref: "main", Verified: true}
	if _, err := ResolveWriteKey(a, intent); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("typed main gained verified authority")
	}
	a.Ref.AppDeliveryKey = "delivery"
	a.Ref.SHA = "sealed-sha"
	verified, err := ResolveWriteKey(a, intent)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := RecordNamespace(manual)
	y, _ := RecordNamespace(verified)
	if x == y {
		t.Fatal("manual write can overwrite verified write")
	}
	intent.Job = "sibling"
	if _, err := ResolveWriteKey(a, intent); !errors.Is(err, ErrDigestNotReadable) {
		t.Fatal("job selected sibling namespace")
	}
}

func TestLogSubjectIdentity(t *testing.T) {
	a := identity().Attempt.Value
	valid := []LogSubject{
		{Kind: LogJobAttempt, Team: a.Team, RunID: a.RunID, Attempt: &a},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &LogUnit{Kind: LogPlan, Generation: 1}},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &LogUnit{Kind: LogTrigger, Generation: 1}},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &LogUnit{Kind: LogEval, Generation: 5, Source: &LogSource{NodeID: "source", Generation: 2}}},
	}
	for _, subject := range valid {
		if err := subject.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	badAttempt := a
	badAttempt.Attempt = 0
	invalid := []LogSubject{
		{},
		{Kind: LogJobAttempt, Team: "foreign", RunID: a.RunID, Attempt: &a},
		{Kind: LogJobAttempt, Team: a.Team, RunID: a.RunID, Attempt: &badAttempt},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Attempt: &a, Unit: &LogUnit{Kind: LogPlan, Generation: 1}},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &LogUnit{Kind: LogEval, Generation: 1}},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &LogUnit{Kind: LogPlan, Generation: 0}},
		{Kind: LogOwnerUnit, Team: a.Team, RunID: a.RunID, Unit: &LogUnit{Kind: LogTrigger, Generation: 1, Source: &LogSource{NodeID: "source", Generation: 1}}},
	}
	for _, subject := range invalid {
		if err := subject.Validate(); err == nil {
			t.Fatalf("accepted %+v", subject)
		}
	}
}

func FuzzDepcacheNamespaceKeepsAbsentRefDistinct(f *testing.F) {
	f.Add("main")
	f.Add("manual:")
	f.Add("../manual:%2F")
	f.Fuzz(func(t *testing.T, name string) {
		key := DepcacheKey{Repo: "local:checkout", Name: "modules", Key: "key", Ref: RefScope{Writer: "account/a"}}
		absent, err := DepcacheNamespace(key)
		if err != nil {
			t.Fatal(err)
		}
		key.Ref = RefScope{Verified: true}
		if _, err := DepcacheNamespace(key); err == nil {
			t.Fatal("empty verified ref accepted")
		}
		for _, verified := range []bool{true, false} {
			key.Ref = RefScope{Ref: name, Verified: verified}
			if !verified {
				key.Ref.Writer = "account/a"
			}
			named, err := DepcacheNamespace(key)
			if err == nil && (name != "" || verified) && named == absent {
				t.Fatal("named ref collided with absent ref")
			}
		}
	})
}

func TestFetchedRunWritesApartFromItsSubmittersUploads(t *testing.T) {
	writer := runspec.PrincipalWriter("account", "alice")
	upload, err := runspec.AssertedProvenance(writer, "refs/heads/main", "0000000000000000000000000000000000000a11", "ci")
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := upload.FetchedFrom(7, "acme/repo", "refs/heads/main", "0000000000000000000000000000000000000a11")
	if err != nil {
		t.Fatal(err)
	}
	if ScopeFor(fetched) == ScopeFor(upload) {
		t.Fatalf("a fetched run restores its submitter's upload caches: %+v", ScopeFor(fetched))
	}
	if fetched.Writer() != writer {
		t.Fatalf("fetched run lost its submitter: %q", fetched.Writer())
	}
}
