package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func zeroEvalFixture(t *testing.T) (*Store, *Tenant, UnitClaim, capability.Issued) {
	t.Helper()
	s, demand := unitClaimFixture(t)
	fundUnitClaim(t, s)
	root := authorityRoot("launcher", "")
	root.Subject.Kind, root.Grants = capability.SubjectService, []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, root)
	demand.CapabilityPrefix = root.Prefix
	demand.Target = &UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 0}
	rewriteSchemaExec(t, s.db, `UPDATE runs SET dispatch='controller'`)
	rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=0,status='done',outcome='memoized',finished_at=1,expansion='expected'`)
	rewriteSchemaExec(t, s.db, `INSERT INTO leases(name,holder,until,generation) VALUES ('controller.owner','controller',?,3)`, s.now().Add(time.Hour).UnixNano())
	rewriteSchemaExec(t, s.db, `INSERT INTO eval_units(team,run_id,source_node_id,source_generation,status,owner_generation) VALUES ('a','run','job',0,'pending',3)`)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	return s, &Tenant{s: s, team: "a"}, claim, issued
}

func TestMemoizedSourceZeroClaimsRenewsAndPublishesEvalLog(t *testing.T) {
	s, tenant, claim, issued := zeroEvalFixture(t)
	if issued.Capability.Binding.Generation != 0 || issued.Capability.Binding.Expansion == nil || issued.Capability.Binding.Expansion.Generation != 1 {
		t.Fatalf("source and independent lease were conflated: %+v", issued.Capability.Binding)
	}
	if _, err := s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true); err != nil {
		t.Fatal(err)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/runs/{run}/jobs/{job}/expansion")
	if !ok {
		t.Fatal("expansion policy unavailable")
	}
	target := capability.Target{Team: "a", RunID: "run", NodeID: "job", SourceGeneration: 0}
	authenticated, err := capability.NewAuthenticator(s.AsOperator()).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: issued.Raw, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capability.Authorize(t.Context(), s.AsOperator(), route, authenticated, target); err != nil {
		t.Fatal(err)
	}
	if err := s.AsOperator().CheckBinding(t.Context(), authenticated.Principal, capability.BindReadSet, target); err != nil {
		t.Fatal(err)
	}
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: int64(claim.Award.Generation), Source: &blob.LogSource{NodeID: "job", Generation: 0}}}
	actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	commit := SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30", Final: true}
	if err := tenant.RecordSegments(t.Context(), commit); err != nil {
		t.Fatal(err)
	}
	reader := authorityMember(t, s, "zero-eval-log-reader", RoleReader)
	got, err := tenant.ReadLogSegments(t.Context(), reader, blob.LogReadRequest{Subject: subject})
	if err != nil || !got.Complete || got.Subject.Unit.Source == nil || got.Subject.Unit.Source.Generation != 0 {
		t.Fatalf("memoized source log read lost explicit zero: %+v %v", got, err)
	}
	prefix, err := blob.LogSubjectPrefix("r30", subject)
	if err != nil || prefix != "logs/r30/teams/a/runs/run/units/eval/job/s0-g1/" {
		t.Fatalf("zero source log namespace=%q: %v", prefix, err)
	}
	for _, subject := range []blob.LogSubject{
		{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 0, Source: &blob.LogSource{NodeID: "job", Generation: 0}}},
		{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: 1}},
	} {
		commit.Subject = subject
		if err := tenant.RecordSegments(t.Context(), commit); err == nil {
			t.Fatal("zero lease or absent source identity authorized eval logs")
		}
	}
}

func FuzzZeroSourceEvalCannotBorrowAnotherFence(f *testing.F) {
	for field := range uint8(5) {
		f.Add(field, uint32(0))
	}
	f.Fuzz(func(t *testing.T, field uint8, value uint32) {
		s, tenant, claim, issued := zeroEvalFixture(t)
		actor := Actor{Kind: ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
		subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "a", RunID: "run", Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: int64(claim.Award.Generation), Source: &blob.LogSource{NodeID: "job", Generation: 0}}}
		switch field % 5 {
		case 0:
			subject.Unit.Source.Generation = 1 + int64(value)
		case 1:
			subject.Unit.Generation += 1 + int64(value)
		case 2:
			subject.Unit.Source.NodeID += fmt.Sprint(value)
		case 3:
			rewriteSchemaExec(t, s.db, `UPDATE nodes SET generation=1`)
		case 4:
			rewriteSchemaExec(t, s.db, `UPDATE eval_units SET generation=generation+1`)
		}
		if err := tenant.RecordSegments(t.Context(), SegmentCommit{Subject: subject, Actor: actor, RetentionClass: "r30", Final: true}); err == nil {
			t.Fatal("eval log borrowed a distinct source or lease fence")
		}
		if field%5 == 3 || field%5 == 4 {
			if _, err := s.AsOperator().RenewUnit(t.Context(), issued.Capability.Prefix, true); err == nil {
				t.Fatal("eval renewal borrowed a distinct source or lease fence")
			}
		}
		rewriteSchemaCount(t, s.db, "log_streams", 0)
	})
}

func TestZeroEvalIdentityRequiresExplicitTaggedSourceGeneration(t *testing.T) {
	unit := UnitRef{Kind: capability.UnitEval, RunID: "run", NodeID: "job", SourceGeneration: 0}
	encoded, err := encodeAwardUnit(unit)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeAwardUnit(capability.UnitEval, encoded); err != nil || got != unit {
		t.Fatalf("explicit source zero: %+v %v", got, err)
	}
	var omitted map[string]any
	if err := json.Unmarshal([]byte(encoded), &omitted); err != nil {
		t.Fatal(err)
	}
	delete(omitted, "SourceGeneration")
	body, err := json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeAwardUnit(capability.UnitEval, string(body)); err == nil {
		t.Fatal("absent generation silently aliased materialized source zero")
	}
}
