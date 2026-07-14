package admission

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func jsonRoundTrip(t *testing.T, snap Snapshot) Snapshot {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return decoded
}

func restoreRoundTrip(t *testing.T, l *Ledger) *Ledger {
	t.Helper()
	snap := l.Snapshot()
	restored, err := Restore(jsonRoundTrip(t, snap), sequentialTokens())
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.Snapshot(); !reflect.DeepEqual(snap, got) {
		t.Fatalf("restored snapshot differs:\n got %+v\nwant %+v", got, snap)
	}
	return restored
}

func busyLedger(t *testing.T) (*Ledger, Lease, Lease) {
	t.Helper()
	l := testLedger(t, 4, 1024)
	parent := mustGrant(t, l, Request{
		ID:          "parent",
		Cores:       1.5,
		MemoryBytes: 256,
		Semaphores:  []SemaphoreClaim{sem("deploy", 2, 1, PolicyQueue)},
	})
	if err := l.Attach(parent.ID, "child"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	victim := mustGrant(t, l, Request{ID: "victim", Semaphores: []SemaphoreClaim{sem("db", 1, 1, PolicyQueue)}})
	d, _ := submit(t, l, Request{ID: "winner", Semaphores: []SemaphoreClaim{sem("db", 1, 1, PolicyCancelOthers)}})
	if d.Kind != DecisionGranted {
		t.Fatalf("winner = %+v", d)
	}
	mustQueue(t, l, Request{ID: "waiter-1", Cores: 4})
	mustQueue(t, l, Request{ID: "waiter-2", Cores: 4, Semaphores: []SemaphoreClaim{sem("deploy", 2, 1, PolicyFail)}})
	if _, err := l.SetHeadroom(3, 512); err != nil {
		t.Fatalf("SetHeadroom: %v", err)
	}
	return l, parent, victim
}

func TestSnapshotRoundTrip_EmptyLedger(t *testing.T) {
	restoreRoundTrip(t, testLedger(t, 4, 1024))
}

func TestSnapshotRoundTrip_BusyLedger(t *testing.T) {
	l, _, _ := busyLedger(t)
	restoreRoundTrip(t, l)
}

func TestSnapshot_IsRepeatable(t *testing.T) {
	l, _, _ := busyLedger(t)
	if !reflect.DeepEqual(l.Snapshot(), l.Snapshot()) {
		t.Fatal("two snapshots of an unchanged ledger differ")
	}
}

func TestRestore_LedgerContinuesExactlyWhereSnapshotLeftOff(t *testing.T) {
	l, parent, victim := busyLedger(t)
	lastSeq := l.Snapshot().EventSeq
	restored := restoreRoundTrip(t, l)

	if id, err := restored.Reattach(parent.Token); err != nil || id != parent.ID {
		t.Fatalf("Reattach after restore = (%s, %v)", id, err)
	}

	events := mustRelease(t, restored, victim.ID, "victim")
	wantKinds(t, events, EventReleased)
	if events[0].Seq != lastSeq+1 {
		t.Fatalf("first post-restore event seq = %d, want %d", events[0].Seq, lastSeq+1)
	}

	events = mustRelease(t, restored, parent.ID, "parent")
	if len(events) != 0 {
		t.Fatalf("parent release with child attached emitted %v", events)
	}
	events = mustRelease(t, restored, parent.ID, "child")
	wantKinds(t, events, EventReleased, EventPromoted)
	if events[1].RequestID != "waiter-1" {
		t.Fatalf("promoted %q, want waiter-1", events[1].RequestID)
	}

	events, err := restored.SetHeadroom(4, 1024)
	if err != nil {
		t.Fatalf("SetHeadroom: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("SetHeadroom after promotion emitted %v", events)
	}
	snap := restored.Snapshot()
	if len(snap.Waiters) != 1 || snap.Waiters[0].RequestID != "waiter-2" {
		t.Fatalf("remaining waiters = %+v, want only waiter-2", snap.Waiters)
	}
}

func TestRestore_MintedLeaseIDsDoNotCollide(t *testing.T) {
	l := testLedger(t, 4, 0)
	first := mustGrant(t, l, Request{ID: "a", Cores: 1})
	restored := restoreRoundTrip(t, l)
	second := mustGrant(t, restored, Request{ID: "b", Cores: 1})
	if second.ID == first.ID {
		t.Fatalf("restored ledger reused lease id %s", first.ID)
	}
}

func TestRestore_AllowsCPUOvercommit(t *testing.T) {
	l := testLedger(t, 24, 8<<30)
	mustGrant(t, l, Request{ID: "first", Cores: 11.2})
	mustGrant(t, l, Request{ID: "second", Cores: 11.2})
	snap := l.Snapshot()
	snap.TotalMilliCores = 14000
	snap.HeadroomMilliCores = 14000

	if _, err := Restore(snap, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

func TestRestore_RejectsCorruptSnapshots(t *testing.T) {
	cases := []struct {
		name    string
		corrupt func(snap *Snapshot)
	}{
		{"negative total cores", func(s *Snapshot) { s.TotalMilliCores = -1 }},
		{"negative headroom", func(s *Snapshot) { s.HeadroomMilliCores = -1 }},
		{"lease with empty member id", func(s *Snapshot) { s.Leases[0].Members[0] = "" }},
		{"lease with negative cores", func(s *Snapshot) { s.Leases[0].MilliCores = -1 }},
		{"lease with duplicate claims", func(s *Snapshot) { s.Leases[0].Claims = append(s.Leases[0].Claims, s.Leases[0].Claims[0]) }},
		{"lease with empty token", func(s *Snapshot) { s.Leases[0].Token = "" }},
		{"duplicate lease id", func(s *Snapshot) { s.Leases[1].ID = s.Leases[0].ID }},
		{"duplicate token", func(s *Snapshot) { s.Leases[1].Token = s.Leases[0].Token }},
		{"lease without members", func(s *Snapshot) { s.Leases[0].Members = nil }},
		{"member appears twice", func(s *Snapshot) { s.Leases[1].Members = append(s.Leases[1].Members, s.Leases[0].Members[0]) }},
		{"lease seq above counter", func(s *Snapshot) { s.Leases[0].Seq = s.LeaseSeq + 1 }},
		{"lease seq reused", func(s *Snapshot) { s.Leases[1].Seq = s.Leases[0].Seq }},
		{"hold on dead lease", func(s *Snapshot) { s.Semaphores[0].Holds[0].Lease = "lease-999" }},
		{"hold without matching claim", func(s *Snapshot) { s.Semaphores[0].Key = "renamed"; s.Leases[0].Claims[0].Key = "renamed" }},
		{"semaphore with no holds", func(s *Snapshot) { s.Semaphores[0].Holds = nil }},
		{"semaphore with empty key", func(s *Snapshot) { s.Semaphores[0].Key = "" }},
		{"duplicate semaphore state", func(s *Snapshot) { s.Semaphores = append(s.Semaphores, s.Semaphores[0]) }},
		{"superseded hold without superseder", func(s *Snapshot) { s.Semaphores[0].Holds[0].SupersededBy = "" }},
		{"live hold naming a superseder", func(s *Snapshot) { s.Semaphores[0].Holds[1].SupersededBy = "lease-1" }},
		{"over-admitted semaphore", func(s *Snapshot) {
			s.Semaphores[0].Holds[0].Superseded = false
			s.Semaphores[0].Holds[0].SupersededBy = ""
		}},
		{"claim cost above capacity", func(s *Snapshot) { s.Leases[0].Claims[0].Cost = 9 }},
		{"waiter with empty id", func(s *Snapshot) { s.Waiters[0].RequestID = "" }},
		{"waiter duplicating a member", func(s *Snapshot) { s.Waiters[0].RequestID = "child" }},
		{"waiter above total cores", func(s *Snapshot) { s.Waiters[0].MilliCores = s.TotalMilliCores + 1 }},
		{"waiter above total memory", func(s *Snapshot) { s.Waiters[1].MemoryBytes = s.TotalMemoryBytes + 1 }},
		{"waiter with duplicate claims", func(s *Snapshot) { s.Waiters[1].Claims = append(s.Waiters[1].Claims, s.Waiters[1].Claims[0]) }},
		{"waiter arrivals out of order", func(s *Snapshot) {
			s.Waiters[0].Arrival, s.Waiters[1].Arrival = s.Waiters[1].Arrival, s.Waiters[0].Arrival
		}},
		{"waiter arrival above counter", func(s *Snapshot) { s.Waiters[1].Arrival = s.ArrivalSeq + 1 }},
		{"stranded promotable waiter", func(s *Snapshot) { s.Waiters[0].MilliCores = 0 }},
		{"used memory above total", func(s *Snapshot) { s.TotalMemoryBytes = 100; s.HeadroomMemoryBytes = 100; s.Waiters = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, _, _ := busyLedger(t)
			snap := l.Snapshot()
			tc.corrupt(&snap)
			if _, err := Restore(snap, nil); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("Restore error = %v, want %v", err, ErrInvalidSnapshot)
			}
		})
	}
}
