package cluster

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// qs builds a QueueState with the two host resource rows the advertisement
// reads, plus a waiter count.
func qs(availCores, capCores float64, availMem, capMem int64, waiters int) wingwire.QueueState {
	q := wingwire.QueueState{
		Resources: []wingwire.ResourceState{
			{Key: "cores", Capacity: capCores, Available: availCores, Reserved: 1},
			{Key: "memory", Capacity: float64(capMem), Available: float64(availMem), Reserved: 1},
		},
	}
	for range waiters {
		q.Waiters = append(q.Waiters, wingwire.Waiter{})
	}
	return q
}

func TestAdvertisedHeadroom_SubtractsAbsoluteReserve(t *testing.T) {
	h := advertisedHeadroom(qs(8, 16, 32<<30, 64<<30, 2), reserve{cores: 2, memoryBytes: 4 << 30})
	if h.Cores != 6 {
		t.Errorf("cores: got %v want 6", h.Cores)
	}
	if h.MemoryBytes != 28<<30 {
		t.Errorf("memory: got %d want %d", h.MemoryBytes, int64(28<<30))
	}
	if h.QueueDepth != 2 {
		t.Errorf("queue depth: got %d want 2", h.QueueDepth)
	}
}

func TestAdvertisedHeadroom_FractionReserveResolvesAgainstMachine(t *testing.T) {
	h := advertisedHeadroom(qs(8, 16, 40<<30, 64<<30, 0), reserve{coresFraction: 0.25, memoryFraction: 0.5})
	if h.Cores != 4 {
		t.Errorf("cores: got %v want 4 (8 available - 0.25*16)", h.Cores)
	}
	if h.MemoryBytes != 8<<30 {
		t.Errorf("memory: got %d want %d (40GiB avail - 0.5*64GiB)", h.MemoryBytes, int64(8<<30))
	}
}

func TestAdvertisedHeadroom_FloorsAtZero(t *testing.T) {
	h := advertisedHeadroom(qs(1, 16, 1<<30, 64<<30, 0), reserve{cores: 4, memoryBytes: 8 << 30})
	if h.Cores != 0 {
		t.Errorf("cores floored: got %v want 0", h.Cores)
	}
	if h.MemoryBytes != 0 {
		t.Errorf("memory floored: got %d want 0", h.MemoryBytes)
	}
}

func TestAdvertisedHeadroom_ShrinksAsAvailableFalls(t *testing.T) {
	full := advertisedHeadroom(qs(16, 16, 64<<30, 64<<30, 0), reserve{})
	held := advertisedHeadroom(qs(6, 16, 20<<30, 64<<30, 0), reserve{})
	if !(held.Cores < full.Cores) {
		t.Errorf("advertised cores should shrink when local runs hold: full=%v held=%v", full.Cores, held.Cores)
	}
	if !(held.MemoryBytes < full.MemoryBytes) {
		t.Errorf("advertised memory should shrink when local runs hold: full=%d held=%d", full.MemoryBytes, held.MemoryBytes)
	}
}

func TestAdvertisedHeadroom_OlderDaemonFallsBackToCapacityMinusHeld(t *testing.T) {
	q := wingwire.QueueState{Resources: []wingwire.ResourceState{
		{Key: "cores", Capacity: 8, Held: 3},
		{Key: "memory", Capacity: float64(16 << 30), Held: float64(4 << 30)},
	}}
	h := advertisedHeadroom(q, reserve{})
	if h.Cores != 5 {
		t.Errorf("cores from capacity-held: got %v want 5", h.Cores)
	}
	if h.MemoryBytes != 12<<30 {
		t.Errorf("memory from capacity-held: got %d want %d", h.MemoryBytes, int64(12<<30))
	}
}

func TestParseReserve_MirrorsBudgetGrammar(t *testing.T) {
	rv, err := parseReserve("2,4gb")
	if err != nil {
		t.Fatalf("parseReserve: %v", err)
	}
	if rv.cores != 2 || rv.memoryBytes != 4<<30 {
		t.Errorf("parsed reserve: got %+v", rv)
	}
	if _, err := parseReserve(""); err != nil {
		t.Errorf("empty reserve should be zero, not error: %v", err)
	}
	if _, err := parseReserve("nonsense-!!"); err == nil {
		t.Error("invalid reserve should error")
	}
}
