package wingd

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestGuardedReleasePersistenceFailureRetainsCapacity(t *testing.T) {
	ledger, err := admission.New(admission.Config{TotalCores: 4, TotalMemoryBytes: 4 << 30})
	if err != nil {
		t.Fatal(err)
	}
	decision, _, err := ledger.Submit(admission.Request{ID: "guarded", Cores: 1})
	if err != nil || decision.Kind != admission.DecisionGranted {
		t.Fatalf("submit guarded lease: decision=%s err=%v", decision.Kind, err)
	}
	session := wingwire.ProcessSession{LeaderPID: 71, SessionID: 71, BirthToken: "birth-71"}
	d := &Daemon{
		layout:              layout{state: filepath.Join(t.TempDir(), "state.json")},
		ledger:              ledger,
		guards:              map[admission.LeaseID]*sessionGuardState{decision.Lease.ID: {persistedGuard: persistedGuard{LeaseID: decision.Lease.ID, RunID: "guarded", Session: session}}},
		byRun:               map[string]*conn{},
		leaseRun:            map[admission.LeaseID]string{decision.Lease.ID: "guarded"},
		leaseCharge:         map[admission.LeaseID]wingwire.HostResources{},
		leaseMembers:        map[admission.LeaseID][]string{decision.Lease.ID: {"guarded"}},
		cancelledRuns:       map[string]struct{}{},
		disconnectedPending: map[string]struct{}{},
	}
	wantErr := errors.New("state unavailable")
	d.persistWrite = func(string, admission.Snapshot, []admissionEvent, []string, []persistedGuard) error {
		return wantErr
	}

	deliveries, released, err := d.releaseGuardDurably(decision.Lease.ID, session)
	if !errors.Is(err, wantErr) || released || len(deliveries) != 0 {
		t.Fatalf("guarded release = deliveries %d released %v err %v", len(deliveries), released, err)
	}
	if _, ok := d.ledger.LeaseByID(decision.Lease.ID); !ok {
		t.Fatal("persistence failure freed guarded capacity in memory")
	}
	if d.guards[decision.Lease.ID] == nil {
		t.Fatal("persistence failure discarded guarded process authority")
	}
}

func TestGuardedReleasePersistsPromotedTokenBeforeDelivery(t *testing.T) {
	ledger, err := admission.New(admission.Config{TotalCores: 1})
	if err != nil {
		t.Fatal(err)
	}
	holder, _, err := ledger.Submit(admission.Request{ID: "guarded", Cores: 1})
	if err != nil || holder.Kind != admission.DecisionGranted {
		t.Fatalf("submit holder: decision=%s err=%v", holder.Kind, err)
	}
	waiter, _, err := ledger.Submit(admission.Request{ID: "follower", Cores: 1})
	if err != nil || waiter.Kind != admission.DecisionQueued {
		t.Fatalf("submit follower: decision=%s err=%v", waiter.Kind, err)
	}
	session := wingwire.ProcessSession{LeaderPID: 72, SessionID: 72, BirthToken: "birth-72"}
	follower := &conn{runID: "follower", role: roleWaiter, resources: wingwire.HostResources{Cores: 1}}
	d := &Daemon{
		layout:              layout{state: filepath.Join(t.TempDir(), "state.json")},
		ledger:              ledger,
		guards:              map[admission.LeaseID]*sessionGuardState{holder.Lease.ID: {persistedGuard: persistedGuard{LeaseID: holder.Lease.ID, RunID: "guarded", Session: session}}},
		byRun:               map[string]*conn{"follower": follower},
		leaseRun:            map[admission.LeaseID]string{holder.Lease.ID: "guarded"},
		leaseCharge:         map[admission.LeaseID]wingwire.HostResources{holder.Lease.ID: {Cores: 1}},
		leaseMembers:        map[admission.LeaseID][]string{holder.Lease.ID: {"guarded"}},
		cancelledRuns:       map[string]struct{}{},
		disconnectedPending: map[string]struct{}{},
	}
	var persisted admission.Snapshot
	d.persistWrite = func(_ string, snap admission.Snapshot, _ []admissionEvent, _ []string, _ []persistedGuard) error {
		persisted = snap
		return nil
	}

	deliveries, released, err := d.releaseGuardDurably(holder.Lease.ID, session)
	if err != nil || !released || len(deliveries) != 1 {
		t.Fatalf("guarded release = deliveries %d released %v err %v", len(deliveries), released, err)
	}
	grant, ok := deliveries[0].msg.(*wingwire.Grant)
	if !ok {
		t.Fatalf("delivery = %T, want grant", deliveries[0].msg)
	}
	var persistedToken string
	for _, lease := range persisted.Leases {
		if lease.RequestID == "follower" {
			persistedToken = lease.Token
		}
	}
	if persistedToken == "" || grant.LeaseToken != persistedToken {
		t.Fatalf("delivered token %q differs from durable token %q", grant.LeaseToken, persistedToken)
	}
}
