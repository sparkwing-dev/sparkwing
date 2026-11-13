package store

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestOwnerClaimTimeSurvivesLeaseSettlement(t *testing.T) {
	s, _, claim, now := launcherSnapshotFixture(t, capability.UnitPlan)
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err := s.db.QueryRow(`SELECT payload FROM events WHERE team='a' AND run_id='run' AND kind='owner_claimed'`).Scan(&body); err != nil {
		t.Fatalf("owner claim time unavailable: %v", err)
	}
	var receipt struct {
		Generation int64     `json:"generation"`
		ClaimedAt  time.Time `json:"claimed_at"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Generation != int64(claim.Award.Generation) || !receipt.ClaimedAt.Equal(*now) {
		t.Fatalf("wrong claim receipt: %s", body)
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

func TestOwnerClaimSurvivesTimingWriteFailure(t *testing.T) {
	s, demand := unitClaimFixture(t)
	demand.Target = &UnitRef{Kind: capability.UnitTrigger, RunID: "run"}
	rewriteSchemaExec(t, s.db, `CREATE TRIGGER reject_startup_timing BEFORE INSERT ON events WHEN NEW.kind='owner_claimed' BEGIN SELECT RAISE(ABORT,'timing sink unavailable'); END`)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
	if err != nil {
		t.Fatalf("timing failure refused claim: %v", err)
	}
	if claim.Award.Generation != 1 {
		t.Fatalf("claim = %+v", claim)
	}
	if !strings.Contains(logs.String(), "Owner claim timing could not be recorded") || !strings.Contains(logs.String(), `"lost_total":1`) {
		t.Fatalf("timing loss was not logged and counted: %s", &logs)
	}
	if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
		t.Fatalf("claim was not committed: %v", err)
	}
}
