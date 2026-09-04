package wingd

import (
	"net"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func handlerDaemon(t *testing.T, cores float64) *Daemon {
	t.Helper()
	return configuredHandlerDaemon(t, Config{}, cores)
}

func configuredHandlerDaemon(t *testing.T, cfg Config, cores float64) *Daemon {
	t.Helper()
	cfg.Home = t.TempDir()
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ledger, err := admission.New(admission.Config{TotalCores: cores})
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	d.ledger = ledger
	d.persistWrite = func(string, admission.Snapshot, []admissionEvent, []string, []persistedGuard) error {
		return nil
	}
	return d
}

func handlerConn(t *testing.T, d *Daemon) (server, peer *conn) {
	t.Helper()
	sc, pc := net.Pipe()
	t.Cleanup(func() { _ = sc.Close(); _ = pc.Close() })
	server = newConn(d, sc)
	server.protocolMajor = guardedSessionMajor
	d.mu.Lock()
	d.conns[server] = struct{}{}
	d.mu.Unlock()
	return server, newConn(d, pc)
}

// safety: a pipe carries no buffer, so the handler cannot return until the
// peer reads the frame it writes.
func callAndRead(t *testing.T, peer *conn, call func()) wingwire.Message {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		call()
	}()
	msg, err := peer.readMessage()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after replying")
	}
	return msg
}

func mustGrantFrame(t *testing.T, msg wingwire.Message) *wingwire.Grant {
	t.Helper()
	grant, ok := msg.(*wingwire.Grant)
	if !ok {
		t.Fatalf("reply = %#v, want a grant", msg)
	}
	return grant
}

func leaseStateFor(t *testing.T, d *Daemon, requestID string) admission.LeaseState {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, lease := range d.ledger.Snapshot().Leases {
		if lease.RequestID == requestID {
			return lease
		}
	}
	t.Fatalf("no lease for %q", requestID)
	return admission.LeaseState{}
}

func TestNestedRunSubLeaseKeepsItsOwnerRun(t *testing.T) {
	d := handlerDaemon(t, 4)

	parent, parentPeer := handlerConn(t, d)
	grant := mustGrantFrame(t, callAndRead(t, parentPeer, func() {
		d.handleAdmission(parent, &wingwire.AdmissionRequest{
			RunID: "parent-run", Resources: wingwire.HostResources{Cores: 1},
		})
	}))

	child, childPeer := handlerConn(t, d)
	callAndRead(t, childPeer, func() {
		d.handleAdmission(child, &wingwire.AdmissionRequest{
			RunID: "child-run", ParentLeaseToken: grant.LeaseToken,
		})
	})

	sub, subPeer := handlerConn(t, d)
	callAndRead(t, subPeer, func() {
		d.handleAdmission(sub, &wingwire.AdmissionRequest{
			RunID: "child-run/plan-sems", OwnerRunID: "child-run", OwnerLeaseToken: grant.LeaseToken,
			DisplayRunID: "child-run", SemaphoresOnly: true, SubLease: true,
			Semaphores: []wingwire.SemaphoreClaim{{Name: "grp", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}},
		})
	})

	if got := leaseStateFor(t, d, "child-run/plan-sems").OwnerID; got != "child-run" {
		t.Fatalf("sub-lease owner = %q, want child-run; the parent's token did not prove the child's ownership", got)
	}
	d.mu.Lock()
	ownerRunID := sub.ownerRunID
	d.mu.Unlock()
	if ownerRunID != "child-run" {
		t.Fatalf("sub-lease connection owner = %q, want child-run", ownerRunID)
	}
}

func TestSubLeaseFromAnUnrelatedRunKeepsNoOwner(t *testing.T) {
	d := handlerDaemon(t, 4)

	holder, holderPeer := handlerConn(t, d)
	grant := mustGrantFrame(t, callAndRead(t, holderPeer, func() {
		d.handleAdmission(holder, &wingwire.AdmissionRequest{
			RunID: "holder-run", Resources: wingwire.HostResources{Cores: 1},
		})
	}))

	other, otherPeer := handlerConn(t, d)
	callAndRead(t, otherPeer, func() {
		d.handleAdmission(other, &wingwire.AdmissionRequest{
			RunID: "other-run", Resources: wingwire.HostResources{Cores: 1},
		})
	})

	sub, subPeer := handlerConn(t, d)
	callAndRead(t, subPeer, func() {
		d.handleAdmission(sub, &wingwire.AdmissionRequest{
			RunID: "other-run/plan-sems", OwnerRunID: "other-run", OwnerLeaseToken: grant.LeaseToken,
			SemaphoresOnly: true, SubLease: true,
			Semaphores: []wingwire.SemaphoreClaim{{Name: "grp", Capacity: 1, Cost: 1, Policy: wingwire.PolicyQueue}},
		})
	})

	if got := leaseStateFor(t, d, "other-run/plan-sems").OwnerID; got != "" {
		t.Fatalf("sub-lease owner = %q, want none; a token from another run proved ownership", got)
	}
}
