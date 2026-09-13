package wingd

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func restartedDaemonHoldingNestedLease(t *testing.T) (*Daemon, string) {
	t.Helper()
	before, err := admission.New(admission.Config{TotalCores: 8, TotalMemoryBytes: 8 << 30})
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	dec, _, err := before.Submit(admission.Request{ID: "parent-run", Cores: 1})
	if err != nil || dec.Kind != admission.DecisionGranted {
		t.Fatalf("grant parent lease: %s %v", dec.Kind, err)
	}
	if err := before.Attach(dec.Lease.ID, "child-run"); err != nil {
		t.Fatalf("attach child: %v", err)
	}

	d, err := New(Config{
		Home: t.TempDir(),
		Sampler: fixedHostSampler{stat: HostStat{
			TotalCores: 8, TotalMemoryBytes: 8 << 30, FreeMemoryBytes: 8 << 30,
			LoadMeasured: true, MemoryMeasured: true,
		}},
	})
	if err != nil {
		t.Fatalf("new daemon: %v", err)
	}
	if err := d.layout.ensureDir(); err != nil {
		t.Fatalf("ensure dir: %v", err)
	}
	if err := writeState(d.layout.state, before.Snapshot(), nil); err != nil {
		t.Fatalf("write state: %v", err)
	}
	if err := d.initLedger(); err != nil {
		t.Fatalf("init ledger: %v", err)
	}
	return d, dec.Lease.Token
}

func heldLease(t *testing.T, d *Daemon) (admission.LeaseState, bool) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	leases := d.ledger.Snapshot().Leases
	if len(leases) == 0 {
		return admission.LeaseState{}, false
	}
	if len(leases) > 1 {
		t.Fatalf("leases = %d, want at most one", len(leases))
	}
	return leases[0], true
}

func TestReattachGivesEachRunOnlyItsOwnMembership(t *testing.T) {
	d, token := restartedDaemonHoldingNestedLease(t)

	parent, parentPeer := handlerConn(t, d)
	first := mustGrantFrame(t, callAndRead(t, parentPeer, func() {
		d.handleReattach(parent, &wingwire.Reattach{LeaseToken: token})
	}))
	if first.RunID != "parent-run" {
		t.Fatalf("first reattach granted %q, want parent-run", first.RunID)
	}
	if len(parent.members) != 1 || parent.members[0] != "parent-run" {
		t.Fatalf("first reattach took members %v, want only parent-run", parent.members)
	}

	child, childPeer := handlerConn(t, d)
	second := mustGrantFrame(t, callAndRead(t, childPeer, func() {
		d.handleReattach(child, &wingwire.Reattach{LeaseToken: token})
	}))
	if second.RunID != "child-run" {
		t.Fatalf("second reattach granted %q, want child-run", second.RunID)
	}
	if len(child.members) != 1 || child.members[0] != "child-run" {
		t.Fatalf("second reattach took members %v, want only child-run", child.members)
	}

	d.handleRelease(child, &wingwire.Release{})
	lease, held := heldLease(t, d)
	if !held {
		t.Fatal("the child's release freed the whole lease while the parent was still running")
	}
	if len(lease.Members) != 1 || lease.Members[0] != "parent-run" {
		t.Fatalf("surviving members = %v, want only parent-run", lease.Members)
	}

	d.handleRelease(parent, &wingwire.Release{})
	if _, held := heldLease(t, d); held {
		t.Fatal("the lease outlived every member")
	}
}

func TestGraceReleasesOnlyTheMembersNoRunReclaimed(t *testing.T) {
	d, token := restartedDaemonHoldingNestedLease(t)

	parent, parentPeer := handlerConn(t, d)
	callAndRead(t, parentPeer, func() {
		d.handleReattach(parent, &wingwire.Reattach{LeaseToken: token})
	})

	d.expireGrace()

	lease, held := heldLease(t, d)
	if !held {
		t.Fatal("grace expiry released a lease a run had already reclaimed")
	}
	if len(lease.Members) != 1 || lease.Members[0] != "parent-run" {
		t.Fatalf("surviving members = %v, want only the reclaimed parent-run", lease.Members)
	}
}

func TestReattachNamingItsRunClaimsThatMemberWhicheverReconnectsFirst(t *testing.T) {
	d, token := restartedDaemonHoldingNestedLease(t)

	child, childPeer := handlerConn(t, d)
	first := mustGrantFrame(t, callAndRead(t, childPeer, func() {
		d.handleReattach(child, &wingwire.Reattach{LeaseToken: token, RunID: "child-run"})
	}))
	if first.RunID != "child-run" {
		t.Fatalf("the child reattached first and was granted %q, want child-run", first.RunID)
	}
	if len(child.members) != 1 || child.members[0] != "child-run" {
		t.Fatalf("the child took members %v, want only child-run", child.members)
	}

	parent, parentPeer := handlerConn(t, d)
	second := mustGrantFrame(t, callAndRead(t, parentPeer, func() {
		d.handleReattach(parent, &wingwire.Reattach{LeaseToken: token, RunID: "parent-run"})
	}))
	if second.RunID != "parent-run" {
		t.Fatalf("the parent reattached second and was granted %q, want parent-run", second.RunID)
	}

	d.mu.Lock()
	holder := d.byRun["parent-run"]
	d.mu.Unlock()
	if holder != parent {
		t.Fatal("parent-run routes to the child's connection, so the child's death cancels the running parent")
	}
}

func TestReattachNamingARunTheLeaseDoesNotHoldIsRefused(t *testing.T) {
	d, token := restartedDaemonHoldingNestedLease(t)

	c, peer := handlerConn(t, d)
	reply := callAndRead(t, peer, func() {
		d.handleReattach(c, &wingwire.Reattach{LeaseToken: token, RunID: "stranger-run"})
	})
	evicted, ok := reply.(*wingwire.Evicted)
	if !ok {
		t.Fatalf("reply = %#v, want an eviction", reply)
	}
	if evicted.Key != "reattach" {
		t.Fatalf("eviction key = %q, want reattach", evicted.Key)
	}
	if len(c.members) != 0 {
		t.Fatalf("a refused reattach took members %v", c.members)
	}

	for _, run := range []string{"parent-run", "child-run"} {
		conn, peer := handlerConn(t, d)
		grant := mustGrantFrame(t, callAndRead(t, peer, func() {
			d.handleReattach(conn, &wingwire.Reattach{LeaseToken: token, RunID: run})
		}))
		if grant.RunID != run {
			t.Fatalf("after the refusal %s was granted %q", run, grant.RunID)
		}
	}
}
