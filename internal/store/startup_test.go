package store

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"
	"time"

	modernsqlite "modernc.org/sqlite"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOwnerClaimTimeSurvivesLeaseSettlement(t *testing.T) {
	s, _, claim, now := launcherSnapshotFixture(t, capability.UnitPlan)
	awardedAt := *now
	*now = now.Add(time.Second)
	exchanged, err := s.AsOperator().ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	claim.Award.CapabilityPrefix = exchanged.Capability.Prefix
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	view, err := (&Tenant{s: s, team: "a"}).ReadRunView(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.OwnerClaims) != 1 || view.OwnerClaims[0].Generation != int64(claim.Award.Generation) || !view.OwnerClaims[0].AwardedAt.Equal(awardedAt) {
		t.Fatalf("wrong owner award timing: %+v", view.OwnerClaims)
	}
}

func TestLauncherStartupRetainsFencedEvidenceOnce(t *testing.T) {
	s, demand, _, now := launcherSnapshotFixture(t, capability.UnitPlan)
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil, nil)
	if err != nil || len(awards) != 1 {
		t.Fatalf("awards=%+v %v", awards, err)
	}
	fence := awards[0].Fence
	startup := map[string]any{"pod_uid": "pod", "scheduled_at": now.Add(-time.Second), "image_pulled_at": *now, "container_started_at": *now}
	body, err := json.Marshal(map[string]any{"award": fence, "startup": startup})
	if err != nil {
		t.Fatal(err)
	}
	var observation capability.LauncherObservation
	if err := json.Unmarshal(body, &observation); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	observation.Startup.ImagePulledAt = nil
	if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE team='a' AND run_id='run' AND kind='cloud_startup'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("startup receipts=%d; want one durable receipt across repeated polls", count)
	}
	observation.Award.Generation++
	_, _, _ = s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, nil, nil, nil, nil)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE team='a' AND run_id='run' AND kind='cloud_startup'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stale award wrote startup evidence: %d %v", count, err)
	}
}

func TestLauncherCannotReportAnotherLaunchersStartup(t *testing.T) {
	s, demand, _, now := launcherSnapshotFixture(t, capability.UnitPlan)
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil, nil)
	if err != nil || len(awards) != 1 {
		t.Fatal(err)
	}
	other := authorityRoot("other-launcher", "")
	other.Subject.Kind = capability.SubjectService
	other.Grants = []capability.Grant{capability.ClaimsLaunch}
	authorityPersist(t, s, other)
	observation := capability.LauncherObservation{Award: awards[0].Fence, Startup: &capability.PodStartup{PodUID: "foreign", ScheduledAt: now}}
	_, _, _ = s.AsOperator().ReconcileLauncherAwards(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: other.Prefix}, []capability.LauncherObservation{observation}, nil, nil, nil, nil)
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE team='a' AND run_id='run' AND kind='cloud_startup'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign launcher contaminated startup: %d %v", count, err)
	}
}

func TestLauncherStartupCanArriveAfterAwardEnds(t *testing.T) {
	s, demand, claim, now := launcherSnapshotFixture(t, capability.UnitPlan)
	root := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: demand.CapabilityPrefix}
	awards, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, nil, nil, nil, nil, nil)
	if err != nil || len(awards) != 1 {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"award": awards[0].Fence, "startup": map[string]any{"pod_uid": "finished-pod", "container_started_at": *now}})
	if err != nil {
		t.Fatal(err)
	}
	var observation capability.LauncherObservation
	if err := json.Unmarshal(body, &observation); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AsOperator().ReconcileLauncherAwards(t.Context(), root, []capability.LauncherObservation{observation}, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE team='a' AND run_id='run' AND kind='cloud_startup'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fast-finished Pod lost startup evidence: %d %v", count, err)
	}
}

func TestOwnerClaimNeverWritesDiagnosticEvents(t *testing.T) {
	s, demand := unitClaimFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER reject_startup_timing BEFORE INSERT ON events WHEN NEW.kind='owner_claimed' BEGIN SELECT RAISE(ABORT,'timing sink unavailable'); END`)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	view, err := (&Tenant{s: s, team: "a"}).ReadRunView(t.Context(), "run")
	if err != nil || len(view.OwnerClaims) != 1 {
		t.Fatalf("owner timing=%+v error=%v", view.OwnerClaims, err)
	}
}

func TestOwnerClaimDeliveryNeverWaitsForDiagnosticSQL(t *testing.T) {
	entered := make(chan struct{}, 1)
	function := "mark_owner_receipt_" + strings.ToLower(rand.Text())
	if err := modernsqlite.RegisterScalarFunction(function, 0, func(*modernsqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		return int64(1), nil
	}); err != nil {
		t.Fatal(err)
	}
	s, demand := unitClaimFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER slow_owner_receipt BEFORE INSERT ON events WHEN NEW.kind='owner_claimed' BEGIN SELECT `+function+`(); SELECT sum(x) FROM (WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT x FROM n); END`)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		claim UnitClaim
		err   error
	}
	done := make(chan result, 2)
	start := make(chan struct{})
	node := demand
	node.Target = &UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}
	for _, request := range []UnitDemand{demand, node} {
		go func() { <-start; claim, err := s.AsOperator().ClaimUnit(ctx, request); done <- result{claim, err} }()
	}
	close(start)
	for range 2 {
		result := <-done
		if result.err != nil {
			t.Errorf("claim could not progress: %v", result.err)
			continue
		}
		if err := s.ReleaseUnit(t.Context(), result.claim, result.claim.Award.CapabilityPrefix); err != nil {
			t.Errorf("award unusable: %v", err)
		}
	}
	select {
	case <-entered:
		t.Error("claim delivery entered slow diagnostic SQL")
	default:
	}
	if ctx.Err() != nil {
		t.Errorf("claims exhausted their safety timeout: %v", ctx.Err())
	}
}
