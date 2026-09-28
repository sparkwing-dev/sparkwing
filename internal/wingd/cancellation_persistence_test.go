package wingd

import (
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestPersistStateSerializesDelayedOlderSnapshotBeforeNewerSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ledger, err := admission.New(admission.Config{TotalCores: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Submit(admission.Request{ID: "first", Cores: 1}); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		layout:        layout{state: path},
		ledger:        ledger,
		cancelledRuns: map[string]struct{}{},
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d.persistWrite = func(path string, snap admission.Snapshot, events []admissionEvent, cancelled []string) error {
		if snap.EventSeq == 1 {
			once.Do(func() { close(entered) })
			<-release
		}
		return writeStateWithCancellations(path, snap, events, cancelled)
	}
	oldDone := make(chan error, 1)
	go func() { oldDone <- d.persistState() }()
	<-entered
	if _, _, err := ledger.Submit(admission.Request{ID: "second", Cores: 1}); err != nil {
		t.Fatal(err)
	}
	newDone := make(chan error, 1)
	go func() { newDone <- d.persistState() }()
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	if err := <-newDone; err != nil {
		t.Fatal(err)
	}
	snap, _, _, err := readStateWithCancellations(path)
	if err != nil {
		t.Fatal(err)
	}
	if snap.EventSeq != 2 {
		t.Fatalf("persisted event sequence = %d, want 2", snap.EventSeq)
	}
}

func TestPersistStateKeepsNewerChildLineageAfterDelayedCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ledger, err := admission.New(admission.Config{TotalCores: 4})
	if err != nil {
		t.Fatal(err)
	}
	dec, _, err := ledger.Submit(admission.Request{ID: "parent", Cores: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Attach(dec.Lease.ID, "first", "parent"); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{layout: layout{state: path}, ledger: ledger}
	resumeOlder := make(chan struct{})
	olderDone := make(chan error, 1)
	go func() {
		<-resumeOlder
		olderDone <- d.persistState()
	}()
	if err := ledger.Attach(dec.Lease.ID, "second", "parent"); err != nil {
		t.Fatal(err)
	}
	if err := d.persistState(); err != nil {
		t.Fatal(err)
	}
	close(resumeOlder)
	if err := <-olderDone; err != nil {
		t.Fatal(err)
	}
	snap, _, _, err := readStateWithCancellations(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Leases) != 1 || len(snap.Leases[0].Members) != 3 || snap.Leases[0].Parents["second"] != "parent" {
		t.Fatalf("persisted lineage = %+v, want both children", snap.Leases)
	}
}

func TestAdmissionChecksDurableTerminalStateAfterTombstoneEviction(t *testing.T) {
	checked := false
	d, err := New(Config{
		Home: t.TempDir(),
		Runs: &FuncRunStore{IsTerminal: func(runID string) (bool, error) {
			checked = true
			return runID == "evicted-cancel", nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, peer := net.Pipe()
	defer peer.Close()
	c := newConn(d, server)
	done := make(chan struct{})
	go func() {
		d.handleAdmission(c, &wingwire.AdmissionRequest{RunID: "evicted-cancel"})
		close(done)
	}()
	msg, err := newConn(d, peer).readMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("durable terminal authority was not checked after cache miss")
	}
	if evicted, ok := msg.(*wingwire.Evicted); !ok || evicted.Key != "cancelled" {
		t.Fatalf("response = %#v, want cancelled eviction", msg)
	}
	<-done
}
