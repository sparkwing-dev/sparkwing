package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

type ledgerTrace struct {
	Events []Event
	Final  Snapshot
}

func TestLedger_RandomSequencesPreserveInvariantsAndConverge(t *testing.T) {
	for seed := int64(0); seed < 25; seed++ {
		t.Run(fmt.Sprintf("seed-%02d", seed), func(t *testing.T) {
			runRandomSequence(t, seed)
		})
	}
}

func TestLedger_RandomSequencesAreDeterministic(t *testing.T) {
	for seed := int64(0); seed < 10; seed++ {
		t.Run(fmt.Sprintf("seed-%02d", seed), func(t *testing.T) {
			first := runRandomSequence(t, seed)
			second := runRandomSequence(t, seed)
			if !reflect.DeepEqual(first, second) {
				t.Fatal("same seed produced a different event trace or final state")
			}
		})
	}
}

func runRandomSequence(t *testing.T, seed int64) ledgerTrace {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	tokens := sequentialTokens()
	totalCores := 1 + float64(rng.Intn(15))*0.5
	totalMemory := uint64(1+rng.Intn(64)) * 128
	l, err := New(Config{TotalCores: totalCores, TotalMemoryBytes: totalMemory, TokenGen: tokens})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	semKeys := []string{"alpha", "beta", "gamma"}
	semCaps := map[string]int{}
	for _, key := range semKeys {
		semCaps[key] = 1 + rng.Intn(4)
	}

	var all []Event
	var lastSeq uint64
	record := func(events []Event) {
		t.Helper()
		for _, ev := range events {
			if ev.Seq != lastSeq+1 {
				t.Fatalf("event seq %d after %d: %+v", ev.Seq, lastSeq, ev)
			}
			lastSeq = ev.Seq
			all = append(all, ev)
		}
	}

	nextID := 0
	for op := 0; op < 300; op++ {
		switch p := rng.Intn(100); {
		case p < 45:
			nextID++
			record(randomSubmit(t, l, rng, nextID, totalCores, totalMemory, semKeys, semCaps))
		case p < 72:
			record(randomRelease(t, l, rng))
		case p < 82:
			nextID++
			randomAttach(t, l, rng, nextID)
		case p < 90:
			events, err := l.SetHeadroom(float64(rng.Intn(int(totalCores*2)+1))*0.5, uint64(rng.Intn(int(totalMemory)+1)))
			if err != nil {
				t.Fatalf("SetHeadroom: %v", err)
			}
			record(events)
		default:
			l = restoredCopy(t, l, tokens)
		}
		l.mustHoldInvariants()
	}

	events, err := l.SetHeadroom(totalCores, totalMemory)
	if err != nil {
		t.Fatalf("SetHeadroom: %v", err)
	}
	record(events)
	for {
		snap := l.Snapshot()
		if len(snap.Leases) == 0 {
			if len(snap.Waiters) != 0 {
				t.Fatalf("empty ledger still queues waiters: %+v", snap.Waiters)
			}
			break
		}
		for _, m := range snap.Leases[0].Members {
			record(mustRelease(t, l, snap.Leases[0].ID, m))
		}
	}
	return ledgerTrace{Events: all, Final: l.Snapshot()}
}

func randomSubmit(t *testing.T, l *Ledger, rng *rand.Rand, n int, totalCores float64, totalMemory uint64, semKeys []string, semCaps map[string]int) []Event {
	t.Helper()
	req := Request{
		ID:          fmt.Sprintf("req-%d", n),
		Cores:       float64(rng.Intn(int(totalCores*2)+2)) * 0.5,
		MemoryBytes: uint64(rng.Intn(int(totalMemory) + 1)),
	}
	policies := []Policy{PolicyQueue, PolicyQueue, PolicyFail, PolicySkip, PolicyCancelOthers}
	for _, i := range rng.Perm(len(semKeys))[:rng.Intn(len(semKeys)+1)] {
		key := semKeys[i]
		capacity := semCaps[key] + rng.Intn(2)
		req.Semaphores = append(req.Semaphores, SemaphoreClaim{
			Key:      key,
			Capacity: capacity,
			Cost:     rng.Intn(capacity + 2),
			Policy:   policies[rng.Intn(len(policies))],
		})
	}
	d, events, err := l.Submit(req)
	if err != nil {
		if !errors.Is(err, ErrNeverAdmissible) {
			t.Fatalf("Submit(%q): %v", req.ID, err)
		}
		return nil
	}
	switch d.Kind {
	case DecisionGranted:
		if d.Lease.ID == "" || d.Lease.Token == "" {
			t.Fatalf("granted without a lease: %+v", d)
		}
	case DecisionQueued:
		if d.Position < 0 {
			t.Fatalf("queued at negative position: %+v", d)
		}
	case DecisionFailed, DecisionSkipped:
		if d.Key == "" {
			t.Fatalf("%s without a key: %+v", d.Kind, d)
		}
	default:
		t.Fatalf("unknown decision kind %q", d.Kind)
	}
	return events
}

func randomRelease(t *testing.T, l *Ledger, rng *rand.Rand) []Event {
	t.Helper()
	snap := l.Snapshot()
	if len(snap.Leases) == 0 {
		return nil
	}
	ls := snap.Leases[rng.Intn(len(snap.Leases))]
	return mustRelease(t, l, ls.ID, ls.Members[rng.Intn(len(ls.Members))])
}

func randomAttach(t *testing.T, l *Ledger, rng *rand.Rand, n int) {
	t.Helper()
	snap := l.Snapshot()
	if len(snap.Leases) == 0 {
		return
	}
	ls := snap.Leases[rng.Intn(len(snap.Leases))]
	if err := l.Attach(ls.ID, fmt.Sprintf("att-%d", n)); err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

func restoredCopy(t *testing.T, l *Ledger, tokens func() string) *Ledger {
	t.Helper()
	snap := l.Snapshot()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	restored, err := Restore(decoded, tokens)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := restored.Snapshot(); !reflect.DeepEqual(snap, got) {
		t.Fatalf("mid-sequence restore diverged:\n got %+v\nwant %+v", got, snap)
	}
	return restored
}
