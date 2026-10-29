package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func creditClaimFixture(t *testing.T, size int, rates store.CreditRateTable) (*engineFixture, *store.Tenant, http.Handler, string) {
	t.Helper()
	e, tenant := engineRuntimeStore(t)
	PaidCreditsFixture(t, e.Store, tenant, 2000)
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(fmt.Sprintf(`{"requires":{"set":true,"value":["sparkwing-%dcpu"]}}`, size)))
	engineAcceptedFixturePlan(t, e, "a", "run")
	if rates != nil {
		teststore.SeedCreditRates(t, e.Store, rates)
	}
	e.claimProfiles = billingCloudProfile{}
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	root := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	h := New(e.Store, nil).WithEngineController(e.EngineController).Handler()
	return e, tenant, h, root
}

func TestCredits_AnUnpricedSizeIsRefusedAtomically(t *testing.T) {
	e, tenant, h, root := creditClaimFixture(t, 32, store.CreditRateTable{{Cores: 2, MicroPerSecond: 10000}, {Cores: 4, MicroPerSecond: 20000}})
	w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job"}`, http.StatusConflict)
	if !strings.Contains(w.Body.String(), unpricedCPUClassCode) {
		t.Fatalf("unpriced refusal: %s", w.Body)
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.Status != runstate.StatusDone || node.Outcome != runstate.NodeFailed || node.ClaimedBy != "" || node.FinishedAt == nil {
		t.Fatalf("unpriced work did not fail fast: %+v: %v", node, err)
	}
	projected, err := teststore.Node(t.Context(), tenant, "run", "job")
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

func TestCredits_ClaimLabelsDoNotLowerTheRequestedSizeBill(t *testing.T) {
	_, tenant, h, root := creditClaimFixture(t, 16, store.CreditRateTable{{Cores: 2, MicroPerSecond: 10000}, {Cores: 4, MicroPerSecond: 20000}, {Cores: 16, MicroPerSecond: 70000}})
	w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job","labels":["sparkwing-2cpu"]}`, http.StatusOK)
	var response controllerwire.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Claim == nil {
		t.Fatalf("missing priced award: %s: %v", w.Body, err)
	}
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].CPUClassCores != 16 || charges[0].RateMicroPerSecond != 70000 || charges[0].AmountMicro != 70000*store.SliceSeconds {
		t.Fatalf("size reservation=%+v: %v", charges, err)
	}
}

func TestCredits_SmallestSizeReservesTwoCoreClass(t *testing.T) {
	_, tenant, h, root := creditClaimFixture(t, 2, nil)
	engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job"}`, http.StatusOK)
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].CPUClassCores != 2 || charges[0].RateMicroPerSecond != 9000 || charges[0].AmountMicro != 9000*store.SliceSeconds {
		t.Fatalf("smallest-size reservation=%+v: %v", charges, err)
	}
}

func TestCredits_MissingLabelCannotClaimByNameOrQueue(t *testing.T) {
	e, tenant, h, root := creditClaimFixture(t, 8, nil)
	engineSQL(t, e.Store, `UPDATE nodes SET execution_json=? WHERE run_id='run' AND node_id='job'`, []byte(`{"requires":{"set":true,"value":["sparkwing-8cpu","tool:terraform"]}}`))
	for _, target := range []string{`,"run_id":"run","job_id":"job"`, ""} {
		status := http.StatusOK
		if target != "" {
			status = http.StatusForbidden
		}
		w := engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"]`+target+`}`, status)
		var response controllerwire.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Claim != nil {
			t.Fatalf("label bypass: %s: %v", w.Body, err)
		}
	}
	node, err := tenant.ReadNode(t.Context(), runspec.NodeRef{RunID: "run", NodeID: "job"})
	if err != nil || node.ClaimedBy != "" || node.Status != runstate.StatusPending {
		t.Fatalf("label refusal mutated node=%+v: %v", node, err)
	}

	engineClaimHTTP(t, h, root, "POST", "/api/v1/launcher/claim", `{"runner_version":"v1","kinds":["node"],"run_id":"run","job_id":"job","labels":["tool:terraform"]}`, http.StatusOK)
	charges, err := tenant.ListCreditCharges(t.Context(), 10)
	if err != nil || len(charges) != 1 || charges[0].CPUClassCores != 8 {
		t.Fatalf("sized reservation=%+v: %v", charges, err)
	}
}
