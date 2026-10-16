package controller

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestEngineLauncherSyncRenewsPreparationBeforeExchange(t *testing.T) {
	e, tenant := engineRuntimeStore(t)
	if _, err := tenant.RecordCreditGrant(t.Context(), store.CreditGrantRequest{Kind: store.CreditGrantFree, AmountMicro: 100_000_000, Reference: "preparation", CreatedBy: "fixture"}); err != nil {
		t.Fatal(err)
	}
	raw := engineHTTPRoot(t, e, "", "launcher", []capability.Grant{capability.ClaimsLaunch})
	engineSQL(t, e.Store, `INSERT INTO nodes(team,run_id,node_id,status,ready_at,spec_json,execution_json) VALUES ('a','run','job','pending',1,?,?)`, []byte(`{"id":"job"}`), []byte(`{}`))
	claim, err := e.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: "launcher", Target: &store.UnitRef{Kind: capability.UnitNode, RunID: "run", NodeID: "job"}, Capacity: match.Resources{Cores: 1}, Available: match.Resources{Cores: 1}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Store.ResolveAuthority(t.Context(), capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix})
	if err != nil {
		t.Fatal(err)
	}
	fence := capability.LeaseRef{Kind: capability.UnitNode, ID: p.Capability.UnitID, Holder: claim.Award.Holder, Generation: claim.Award.Generation}
	handler := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil))).WithEngineController(e).Handler()
	for exchange := range 2 {
		original := time.Now().Add(10 * time.Second)
		engineSQL(t, e.Store, `UPDATE nodes SET lease_expires_at=? WHERE run_id='run' AND node_id='job'`, original.UnixNano())
		body, err := json.Marshal(controllerwire.LauncherSyncRequest{Preparing: []capability.LeaseRef{fence}})
		if err != nil {
			t.Fatal(err)
		}
		if exchange == 0 {
			malformed, err := json.Marshal(controllerwire.LauncherSyncRequest{Preparing: []capability.LeaseRef{fence}, Observed: []controllerwire.LauncherObservation{{}}})
			if err != nil {
				t.Fatal(err)
			}
			engineClaimHTTP(t, handler, raw, "POST", "/api/v1/launcher/sync", string(malformed), 400)
			unchanged, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
			if err != nil || unchanged.LeaseExpiresAt == nil || !unchanged.LeaseExpiresAt.Equal(original) {
				t.Fatal("malformed observation mutated preparation lease")
			}
		}
		engineClaimHTTP(t, handler, raw, "POST", "/api/v1/launcher/sync", string(body), 200)
		node, err := tenant.ReadNode(t.Context(), store.NodeRef{RunID: "run", NodeID: "job"})
		if err != nil {
			t.Fatal(err)
		}
		if exchange == 0 {
			if node.LeaseExpiresAt == nil || !node.LeaseExpiresAt.After(original.Add(limits.DefaultLease/2)) {
				t.Fatalf("preparation did not renew: %+v", node.LeaseExpiresAt)
			}
			if _, err = e.Store.ExchangeCapability(t.Context(), capability.ExchangeSpec{BootstrapPrefix: claim.Award.CapabilityPrefix}); err != nil {
				t.Fatal(err)
			}
		} else if node.LeaseExpiresAt == nil || !node.LeaseExpiresAt.Equal(original) {
			t.Fatal("launcher renewed after exchange")
		}
	}
}
