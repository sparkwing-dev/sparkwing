package controller

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestUnitEligibilityAppliesClaimGrantsAndCapacity(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	e.Store.SetClaimProfileResolver(engineProfileFake{})
	e.RunnerPolicy = runnerAdmissionFunc(func(context.Context, string) error { return nil })
	for node, cores := range map[string]float64{"small": 1, "big": 4} {
		execution, err := json.Marshal(runspec.JobExecutionSettings{Resources: runspec.Setting[runspec.Resources]{Optional: runspec.Optional[runspec.Resources]{Set: true, Value: runspec.Resources{Cores: runspec.Setting[float64]{Optional: runspec.Optional[float64]{Set: true, Value: cores}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run',?,'pending',1,?,?)`, node, []byte(`{"id":"`+node+`"}`), execution)
	}
	h := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	eligible := func(unit store.UnclaimedUnit) bool {
		t.Helper()
		ok, err := tenant.UnitHasEligibleClaimant(t.Context(), "run", unit, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	trigger, small, big := store.UnclaimedUnit{Kind: capability.UnitTrigger}, store.UnclaimedUnit{Kind: capability.UnitNode, NodeID: "small"}, store.UnclaimedUnit{Kind: capability.UnitNode, NodeID: "big"}
	nodes := engineHTTPRoot(t, e, "a", "nodes-only", []capability.Grant{capability.NodesClaim})
	poll := func(raw, kind string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/v1/runners/claim", strings.NewReader(`{"runner_version":"v1","kinds":["`+kind+`"],"available_cores":0}`))
		r.Header.Set("Authorization", "Bearer "+raw)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var response controllerwire.ClaimResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Claim != nil {
			t.Fatalf("busy %s poll: %d %s", kind, w.Code, w.Body)
		}
	}
	poll(nodes, "node")
	if eligible(trigger) {
		t.Fatal("a nodes.claim-only agent counts as eligible for a trigger")
	}
	if !eligible(small) {
		t.Fatal("a nodes.claim agent that fits is not eligible for a job")
	}
	if eligible(big) {
		t.Fatal("an agent smaller than the job counts as eligible")
	}
	triggers := engineHTTPRoot(t, e, "a", "triggers", []capability.Grant{capability.TriggersClaim})
	engineSQL(t, e.Store, `UPDATE triggers SET available_at=? WHERE id='run'`, time.Now().Add(time.Hour).UnixNano())
	poll(triggers, "trigger")
	engineSQL(t, e.Store, `UPDATE triggers SET available_at=1 WHERE id='run'`)
	if !eligible(trigger) {
		t.Fatal("a polling triggers.claim agent is not eligible for the trigger")
	}
}
