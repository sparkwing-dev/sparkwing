package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type creditClaimProfile struct{ cores float64 }

func (p creditClaimProfile) ResolveClaimProfile(context.Context, capability.Principal) (match.Profile, match.Resources, error) {
	capacity := match.Resources{Cores: p.cores}
	return match.Profile{Capacity: capacity}, capacity, nil
}

func creditClaimFixture(t *testing.T, cores float64, rates store.CreditRateTable) (*EngineController, *store.Tenant, http.Handler, string) {
	t.Helper()
	e, tenant := engineRuntimeStore(t)
	PaidCreditsFixture(t, e.Store, tenant, 2000)
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(fmt.Sprintf(`{"resources":{"set":true,"value":{"cores":{"set":true,"value":%g}}}}`, cores)))
	if rates != nil {
		if _, err := e.Store.AsOperator().SetCreditSettings(t.Context(), store.CreditSettingsUpdate{RateTable: &rates}); err != nil {
			t.Fatal(err)
		}
	}
	e.Store.SetClaimProfileResolver(creditClaimProfile{128})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h := New(e.Store, nil).WithEngineController(e).Handler()
	return e, tenant, h, root
}

func TestCredits_AClaimAboveTheLargestClassIsRefusedAtomically(t *testing.T) {
	e, tenant, h, root := creditClaimFixture(t, 96, store.CreditRateTable{{Cores: 2, MicroPerSecond: 10000}, {Cores: 4, MicroPerSecond: 20000}})
	w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job","available_cores":128}`, http.StatusConflict)
	if !strings.Contains(w.Body.String(), UnpricedCPUClassCode) {
		t.Fatalf("unpriced refusal: %s", w.Body)
	}
	node, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.Status != store.StatusDone || node.Outcome != store.NodeFailed || node.ClaimedBy != "" || node.FinishedAt == nil {
		t.Fatalf("unpriced work did not fail fast: %+v: %v", node, err)
	}
	projected, err := tenant.GetNode(t.Context(), "run", "job")
	if err != nil || projected.FailureReason != store.FailureUnpricedCPUClass {
		t.Fatalf("pricing failure reason: %+v %v", projected, err)
	}
	for _, table := range []string{"credit_charges", "billing_awards", "node_attempts"} {
		var count int
		if err := teststore.SQL(t, e.Store).QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows=%d: %v", table, count, err)
		}
	}
}

func TestCredits_AClaimsOwnCPUFigureDoesNotLowerTheBill(t *testing.T) {
	property := func(value uint16) bool {
		_, tenant, h, root := creditClaimFixture(t, 16, store.CreditRateTable{{Cores: 2, MicroPerSecond: 10000}, {Cores: 4, MicroPerSecond: 20000}, {Cores: 16, MicroPerSecond: 70000}})
		w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", fmt.Sprintf(`{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job","available_cores":128,"capacity":{"cores":%g}}`, float64(value)/100), http.StatusOK)
		var response controllerwire.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Claim == nil {
			t.Fatalf("missing priced award: %s: %v", w.Body, err)
		}
		charges, err := tenant.ListCreditCharges(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(charges) == 1 && charges[0].CPUClassCores == 16 && charges[0].RateMicroPerSecond == 70000 && charges[0].AmountMicro == 70000*store.SliceSeconds
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestCredits_QuarterCorePinReservesTwoCoreMinimum(t *testing.T) {
	_, tenant, h, root := creditClaimFixture(t, 0.25, nil)
	engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job","available_cores":128}`, http.StatusOK)
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].CPUClassCores != 2 || charges[0].RateMicroPerSecond != 9000 || charges[0].AmountMicro != 9000*store.SliceSeconds {
		t.Fatalf("quarter-core reservation=%+v: %v", charges, err)
	}
}

func TestCredits_InsufficientCapacityCannotClaimByNameOrQueue(t *testing.T) {
	e, tenant, h, root := creditClaimFixture(t, 8, nil)
	e.Store.SetClaimProfileResolver(creditClaimProfile{2})
	for _, target := range []string{`"run_id":"run","job_id":"job",`, ""} {
		status := http.StatusOK
		if target != "" {
			status = http.StatusForbidden
		}
		w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],`+target+`"available_cores":128}`, status)
		var response controllerwire.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Claim != nil {
			t.Fatalf("capacity bypass: %s: %v", w.Body, err)
		}
	}
	node, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.ClaimedBy != "" || node.Status != store.StatusPending {
		t.Fatalf("capacity refusal mutated node=%+v: %v", node, err)
	}
	e.Store.SetClaimProfileResolver(creditClaimProfile{8})
	engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job","available_cores":8}`, http.StatusOK)
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].CPUClassCores != 8 {
		t.Fatalf("sized reservation=%+v: %v", charges, err)
	}
}
