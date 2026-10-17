package store_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

var _ controller.ConcurrencyStore = (*store.Tenant)(nil)

func slotFixture(t *testing.T) *storeRecoveryFixture {
	t.Helper()
	return newStoreRecoveryFixtureAt(t, true, storetest.NewSQLite(t), func(doc *nodeproto.PlanDoc) {
		scope, capacity, cost, policy := "global", 3, 2, "queue"
		doc.Jobs[0].Concurrency = []nodeproto.ConcurrencySpec{{Group: "deploy", Scope: &scope, Capacity: &capacity, Cost: &cost, OnLimit: &policy}}
	})
}

func slotAuthorization(t *testing.T, f *storeRecoveryFixture, operation, id string) capability.Authorized {
	t.Helper()
	method := "POST"
	if operation == "" || operation == "/holder" || operation == "/resolve" {
		method = "GET"
	}
	route, ok := capability.LookupRoute(method, "/api/v1/queue/groups/{group}"+operation)
	if !ok {
		t.Fatal("missing slot route")
	}
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	target := capability.Target{Team: string(f.tenant.Team()), RunID: f.ref.RunID, NodeID: f.ref.NodeID, SlotKey: "global:deploy", SlotHolderID: id, SlotRunID: f.ref.RunID, SlotNodeID: f.ref.NodeID, SlotGeneration: row.Generation}
	auth, err := capability.NewAuthenticator(f.s).Authenticate(t.Context(), capability.Presentation{Raw: f.holderBearer, Class: capability.CredentialBearer, Evidence: capability.RequestEvidence{Target: target}}, route)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), f.s, route, auth, target)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func slotRequest(f *storeRecoveryFixture, id string) store.AcquireSlotRequest {
	return store.AcquireSlotRequest{Key: "global:deploy", HolderID: id, RunID: f.ref.RunID, NodeID: f.ref.NodeID, Lease: time.Hour}
}

func TestAuthorizedConcurrencyPreservesDeclarationAndAuthorityProperty(t *testing.T) {
	f := slotFixture(t)
	a := slotAuthorization(t, f, "/acquire", "holder")
	random := rand.New(rand.NewPCG(89721, 6521))
	for i := range 112 {
		altered, req := a, slotRequest(f, "holder")
		hostile := fmt.Sprintf("%s/%x", []string{"' OR 1=1 --", "雪", ":_:%", "foreign"}[random.IntN(4)], random.Uint64())
		switch i % 14 {
		case 0:
			altered.Target.Team = hostile
		case 1:
			altered.Binding.RunID = hostile
		case 2:
			altered.Binding.NodeID = hostile
		case 3:
			altered.Binding.Generation += 1 + random.Int64N(9999)
		case 4:
			altered.Binding.Holder = hostile
		case 5:
			altered.Target.SlotKey = hostile
			req.Key = hostile
		case 6:
			req.Capacity = 4 + random.IntN(9999)
		case 7:
			req.Cost = 1
		case 8:
			req.Policy = "cancel_others"
		case 9:
			req.CacheKeyHash = hostile
		case 10:
			req.InheritedHolderID = hostile
		case 11:
			altered.Principal.Authority.ID = hostile
		case 12:
			altered.Access.Grants = []capability.Grant{capability.RunsRead}
		case 13:
			req.RunID = hostile
		}
		if response, err := f.tenant.AcquireAuthorizedSlot(t.Context(), altered, req); err == nil {
			t.Fatalf("forgery %d admitted: %+v", i, response)
		}
	}
	var count int
	if err := f.s.DB().QueryRow(`SELECT COUNT(*) FROM concurrency_holders`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused inputs changed capacity: %d, %v", count, err)
	}
	got, err := f.tenant.AcquireAuthorizedSlot(t.Context(), a, slotRequest(f, "holder"))
	if err != nil || got.Kind != store.AcquireGranted {
		t.Fatalf("declared acquisition: %+v, %v", got, err)
	}
	row, err := f.tenant.ReadNode(t.Context(), f.ref)
	if err != nil || row.LeaseExpiresAt == nil || got.LeaseExpiresAt.After(*row.LeaseExpiresAt) {
		t.Fatalf("capacity outlived job lease: %+v, %+v, %v", got, row.LeaseExpiresAt, err)
	}
	state, err := f.tenant.ReadAuthorizedGroup(t.Context(), slotAuthorization(t, f, "", ""), "global:deploy")
	if err != nil || state.Capacity != 3 || state.UsedCost != 2 {
		t.Fatalf("request replaced declaration: %+v, %v", state, err)
	}
	read := slotAuthorization(t, f, "/holder", "holder")
	if holder, err := f.tenant.ReadAuthorizedHolder(t.Context(), read, "global:deploy", "holder"); err != nil || holder.RunID != f.ref.RunID || holder.Cost != 2 {
		t.Fatalf("read own holder: %+v, %v", holder, err)
	}
	before := *state
	for range 32 {
		altered := read
		altered.Target.SlotHolderID = fmt.Sprintf("holder-%x", random.Uint64())
		if _, err := f.tenant.ReadAuthorizedHolder(t.Context(), altered, "global:deploy", altered.Target.SlotHolderID); err == nil {
			t.Fatal("read an unowned holder")
		}
	}
	after, err := f.tenant.GetConcurrencyState(t.Context(), "global:deploy")
	if err != nil || !reflect.DeepEqual(&before, after) {
		t.Fatalf("refused read changed group: %+v, %v", after, err)
	}
}

func TestAuthorizedConcurrencyCancellationKeepsReportingAndRevocationRefusesAll(t *testing.T) {
	f := slotFixture(t)
	acquire := slotAuthorization(t, f, "/acquire", "holder")
	if _, err := f.tenant.AcquireAuthorizedSlot(t.Context(), acquire, slotRequest(f, "holder")); err != nil {
		t.Fatal(err)
	}
	heartbeat := slotAuthorization(t, f, "/heartbeat", "holder")
	release := slotAuthorization(t, f, "/release", "holder")
	resolve := slotAuthorization(t, f, "/resolve", "")
	cancel := slotAuthorization(t, f, "/cancel-waiter", "")
	if _, err := f.tenant.TransitionNode(t.Context(), store.NodeTransition{Ref: f.ref, Actor: f.owner, To: store.NodeCancelled, Cause: store.CauseCancel, Extra: store.TransitionExtra{Result: &store.NodeResult{Outcome: store.NodeCancelled, Reason: "stop"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.AcquireAuthorizedSlot(t.Context(), acquire, slotRequest(f, "holder")); err == nil {
		t.Fatal("cancelled claim reacquired capacity")
	}
	if _, err := f.tenant.ResolveAuthorizedWaiter(t.Context(), resolve, "global:deploy", f.ref.RunID, f.ref.NodeID, "", "", "", false); err == nil {
		t.Fatal("cancelled claim resolved sensitive capacity")
	}
	until, _, err := f.tenant.HeartbeatAuthorizedSlot(t.Context(), heartbeat, "global:deploy", "holder", time.Hour)
	if err != nil || until.IsZero() {
		t.Fatalf("cancelled claim cannot report: %s, %v", until, err)
	}
	if err := f.tenant.ReleaseAuthorizedSlot(t.Context(), release, "global:deploy", "holder", "cancelled", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.CancelAuthorizedWaiter(t.Context(), cancel, "global:deploy", f.ref); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.ResolveAuthority(t.Context(), f.holder.Authority)
	if err != nil || p.Capability.Parent == nil {
		t.Fatalf("claim ancestry: %+v, %v", p, err)
	}
	if err := f.tenant.RevokeAuthority(t.Context(), store.Actor{Kind: store.ActorOperator, Authority: f.plan.Seal.Request.AuthorityRef}, *p.Capability.Parent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.tenant.HeartbeatAuthorizedSlot(t.Context(), heartbeat, "global:deploy", "holder", time.Hour); err == nil {
		t.Fatal("revoked claim renewed capacity")
	}
	if _, err := f.tenant.CancelAuthorizedWaiter(t.Context(), cancel, "global:deploy", f.ref); err == nil {
		t.Fatal("revoked claim cancelled capacity")
	}
}

func TestAuthorizedConcurrencyRefusesForeignHolderGenerationAndReadEdges(t *testing.T) {
	f := slotFixture(t)
	acquire := slotAuthorization(t, f, "/acquire", "holder")
	competitor, err := f.capacityCompetitor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	foreign := slotRequest(f, "holder")
	foreign.RunID, foreign.Capacity, foreign.Cost = competitor, 3, 2
	if _, err := f.tenant.AcquireConcurrencySlot(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.AcquireAuthorizedSlot(t.Context(), acquire, slotRequest(f, "holder")); err == nil {
		t.Fatal("claim replaced another run's holder")
	}
	resolve := slotAuthorization(t, f, "/resolve", "")
	random := rand.New(rand.NewPCG(7311, 9914))
	for range 32 {
		run, job := competitor, fmt.Sprintf("job/%x", random.Uint64())
		if _, err := f.tenant.ResolveAuthorizedWaiter(t.Context(), resolve, "global:deploy", f.ref.RunID, f.ref.NodeID, "", run, job, false); err == nil {
			t.Fatal("undeclared leader edge resolved")
		}
	}
	if _, err := f.tenant.ReleaseConcurrencySlot(t.Context(), "global:deploy", "holder", "cancelled", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.AcquireAuthorizedSlot(t.Context(), acquire, slotRequest(f, "holder")); err != nil {
		t.Fatal(err)
	}
	heartbeat := slotAuthorization(t, f, "/heartbeat", "holder")
	if _, err := f.s.DB().ExecContext(t.Context(), `UPDATE concurrency_holders SET generation=generation+1 WHERE holder_id='holder'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.tenant.HeartbeatAuthorizedSlot(t.Context(), heartbeat, "global:deploy", "holder", time.Hour); err == nil {
		t.Fatal("capacity generation was silently adopted")
	}
}

func TestAuthorizedConcurrencyRechecksAuthorityBeforeCommit(t *testing.T) {
	for _, mutation := range []string{
		`UPDATE capabilities SET revoked_at=1 WHERE prefix IN (SELECT capability_prefix FROM nodes WHERE team=NEW.team AND run_id=NEW.run_id AND node_id=NEW.node_id)`,
		`UPDATE nodes SET generation=generation+1 WHERE team=NEW.team AND run_id=NEW.run_id AND node_id=NEW.node_id`,
		`UPDATE nodes SET claimed_by='replaced' WHERE team=NEW.team AND run_id=NEW.run_id AND node_id=NEW.node_id`,
	} {
		t.Run(mutation, func(t *testing.T) {
			f := slotFixture(t)
			a := slotAuthorization(t, f, "/acquire", "holder")
			if _, err := f.s.DB().ExecContext(t.Context(), `CREATE TRIGGER change_slot_authority AFTER INSERT ON concurrency_holders BEGIN `+mutation+`; END`); err != nil {
				t.Fatal(err)
			}
			response, err := f.tenant.AcquireAuthorizedSlot(t.Context(), a, slotRequest(f, "holder"))
			if err == nil || !reflect.DeepEqual(response, store.AcquireSlotResponse{}) {
				t.Fatalf("authority changed before commit: %+v, %v", response, err)
			}
			var count int
			if err := f.s.DB().QueryRow(`SELECT COUNT(*) FROM concurrency_holders`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed authority race left capacity: %d, %v", count, err)
			}
		})
	}
}

func TestAuthorizedConcurrencyMemberForceReleasePromotesWithinWorkLease(t *testing.T) {
	f := slotFixture(t)
	if _, err := f.tenant.AcquireAuthorizedSlot(t.Context(), slotAuthorization(t, f, "/acquire", "holder"), slotRequest(f, "holder")); err != nil {
		t.Fatal(err)
	}
	competitor, err := f.capacityCompetitor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	request := slotRequest(f, "waiting")
	request.RunID, request.Capacity, request.Cost = competitor, 3, 2
	if response, err := f.tenant.AcquireConcurrencySlot(t.Context(), request); err != nil || response.Kind != store.AcquireQueued {
		t.Fatalf("competitor queued: %+v, %v", response, err)
	}
	if _, err := f.s.DB().ExecContext(t.Context(), `UPDATE concurrency_holders SET superseded=1 WHERE holder_id='holder'`); err != nil {
		t.Fatal(err)
	}
	route, ok := capability.LookupRoute("POST", "/api/v1/queue/groups/{group}/force-release")
	if !ok {
		t.Fatal("missing force-release route")
	}
	p, err := f.s.ResolveAuthority(t.Context(), f.plan.Seal.Request.AuthorityRef)
	if err != nil {
		t.Fatal(err)
	}
	a, err := capability.Authorize(t.Context(), f.s, route, capability.Authenticated{Principal: p, Method: route.Method, Pattern: route.Path, Live: []capability.LiveAccess{{RuleIndex: 0}}}, capability.Target{Team: string(f.tenant.Team()), SlotKey: "global:deploy"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.tenant.ForceReleaseAuthorizedSlots(t.Context(), a, "global:deploy")
	if err != nil || len(out) != 1 || out[0].HolderID != "holder" {
		t.Fatalf("force release: %+v, %v", out, err)
	}
	state, err := f.tenant.GetConcurrencyState(t.Context(), "global:deploy")
	if err != nil || len(state.Holders) != 1 || state.Holders[0].RunID != competitor || len(state.Waiters) != 0 {
		t.Fatalf("force release did not atomically promote: %+v, %v", state, err)
	}
	var ownerLease int64
	if err := f.s.DB().QueryRow(`SELECT lease_expires_at FROM triggers WHERE id=?`, competitor).Scan(&ownerLease); err != nil || state.Holders[0].LeaseExpiresAt.After(time.Unix(0, ownerLease)) {
		t.Fatalf("promoted beyond recipient work lease: %+v, %v", state.Holders[0], err)
	}
	if out, err := f.tenant.ForceReleaseAuthorizedSlots(t.Context(), a, "global:deploy"); err != nil || len(out) != 0 {
		t.Fatalf("idempotent force release: %+v, %v", out, err)
	}
	if _, err := f.s.DB().ExecContext(t.Context(), `UPDATE memberships SET role='reader' WHERE team=?`, f.tenant.Team()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tenant.ForceReleaseAuthorizedSlots(t.Context(), a, "global:deploy"); err == nil {
		t.Fatal("former controller force released capacity")
	}
}

func TestAuthorizedConcurrencyGroupDoesNotDiscloseUndeclaredReadEdgesProperty(t *testing.T) {
	f := slotFixture(t)
	competitor, err := f.capacityCompetitor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewPCG(88116, 7622))
	for i := range 32 {
		leaderRun := competitor
		if i%2 == 0 {
			leaderRun = fmt.Sprintf("foreign/%x", random.Uint64())
		}
		cache := fmt.Sprintf("memo/%x", random.Uint64())
		if _, err := f.s.DB().ExecContext(t.Context(), `INSERT INTO concurrency_entries(team,key,capacity,updated_at) VALUES (?,'global:deploy',3,1) ON CONFLICT(team,key) DO NOTHING`, f.tenant.Team()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.DB().ExecContext(t.Context(), `INSERT INTO concurrency_waiters(team,key,run_id,node_id,holder_id,arrived_at,policy,cache_key_hash,leader_run_id,leader_node_id,generation,lease_expires_at) VALUES (?,'global:deploy',?,'job',?,?,'queue',?,?,'job',0,?)`, f.tenant.Team(), competitor, fmt.Sprintf("wait-%d", i), i+1, cache, leaderRun, time.Now().Add(time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
		state, err := f.tenant.ReadAuthorizedGroup(t.Context(), slotAuthorization(t, f, "", ""), "global:deploy")
		if err != nil || len(state.Waiters) != 1 {
			t.Fatalf("group view unavailable: %+v, %v", state, err)
		}
		waiter := state.Waiters[0]
		if waiter.RunID != competitor || waiter.LeaderRunID != "" || waiter.LeaderNodeID != "" || waiter.CacheKeyHash != "" {
			t.Fatalf("group disclosed undeclared edge: %+v", waiter)
		}
		if _, err := f.s.DB().ExecContext(t.Context(), `DELETE FROM concurrency_waiters WHERE team=? AND key='global:deploy'`, f.tenant.Team()); err != nil {
			t.Fatal(err)
		}
	}
}
