package wingd

import (
	"bytes"
	"net"
	"os"
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
	enteredOlder := make(chan struct{})
	resumeOlder := make(chan struct{})
	d.persistWrite = func(path string, snap admission.Snapshot, events []admissionEvent, cancelled []string) error {
		if len(snap.Leases[0].Members) == 2 {
			close(enteredOlder)
			<-resumeOlder
		}
		return writeStateWithCancellations(path, snap, events, cancelled)
	}
	olderDone := make(chan error, 1)
	go func() { olderDone <- d.persistState() }()
	<-enteredOlder
	if err := ledger.Attach(dec.Lease.ID, "second", "parent"); err != nil {
		t.Fatal(err)
	}
	newerDone := make(chan error, 1)
	go func() { newerDone <- d.persistState() }()
	close(resumeOlder)
	if err := <-olderDone; err != nil {
		t.Fatal(err)
	}
	if err := <-newerDone; err != nil {
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

func TestRestoreStateWithoutParentsKeepsFlatRootLineage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ledger, err := admission.New(admission.Config{TotalCores: 4})
	if err != nil {
		t.Fatal(err)
	}
	dec, _, err := ledger.Submit(admission.Request{ID: "root", Cores: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"child", "grandchild"} {
		if err := ledger.Attach(dec.Lease.ID, id, "root"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ledger.Release(dec.Lease.ID, "root"); err != nil {
		t.Fatal(err)
	}
	snap := ledger.Snapshot()
	snap.Leases[0].Parents = nil
	if err := writeState(path, snap, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || bytes.Contains(data, []byte(`"parents"`)) {
		t.Fatal("legacy state file unexpectedly has parents")
	}
	read, _, err := readState(path)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := admission.Restore(*read, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"child", "grandchild"} {
		if got := restored.Snapshot().Leases[0].Parents[id]; got != "root" {
			t.Fatalf("%s parent = %q, want root", id, got)
		}
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
