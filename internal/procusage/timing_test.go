package procusage

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

type referenceScan struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type referenceCPUTiming struct {
	PreviousScan referenceScan `json:"previous_scan"`
	CurrentScan  referenceScan `json:"current_scan"`
}

func checkCPUTiming(t *testing.T, reading Reading, want *referenceCPUTiming) {
	t.Helper()
	encoded, err := json.Marshal(reading)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Timing *referenceCPUTiming `json:"cpu_rate_timing"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wire.Timing, want) {
		t.Fatalf("CPU scan timing = %+v, want %+v", wire.Timing, want)
	}
}

func TestTreeRetainsConsecutiveCPUScanWindows(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	read := func(from, to, cpu time.Duration) Snapshot {
		snapshot := fixtureSnapshot(start.Add(from), fixtureProcess(10, 1, 10, cpu, 0, 4096, start.Add(to)))
		snapshot.End = start.Add(to)
		return snapshot
	}
	tree := Tree{PID: 10}
	baseline := tree.Observe(read(0, 500*time.Millisecond, 0))
	checkCPUTiming(t, baseline, nil)
	second := tree.Observe(read(time.Second, 1500*time.Millisecond, time.Second))
	if second.CPUMillicores != 1000 || second.CPUQuality != "sampled" {
		t.Fatalf("second CPU reading: %+v", second)
	}
	checkCPUTiming(t, second, &referenceCPUTiming{
		PreviousScan: referenceScan{Start: start, End: start.Add(500 * time.Millisecond)},
		CurrentScan:  referenceScan{Start: start.Add(time.Second), End: start.Add(1500 * time.Millisecond)},
	})
	third := tree.Observe(read(2*time.Second, 2250*time.Millisecond, 1750*time.Millisecond))
	if third.CPUMillicores != 1000 || third.CPUQuality != "sampled" {
		t.Fatalf("third CPU reading: %+v", third)
	}
	checkCPUTiming(t, third, &referenceCPUTiming{
		PreviousScan: referenceScan{Start: start.Add(time.Second), End: start.Add(1500 * time.Millisecond)},
		CurrentScan:  referenceScan{Start: start.Add(2 * time.Second), End: start.Add(2250 * time.Millisecond)},
	})
	if !second.Start.Equal(start) || !second.End.Equal(start.Add(1500*time.Millisecond)) || !third.Start.Equal(start.Add(time.Second)) || !third.End.Equal(start.Add(2250*time.Millisecond)) {
		t.Fatal("scan envelopes were replaced by narrower representative windows")
	}
}

func TestTreeCPUScanTimingDoesNotBridgeFailedBaseline(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tree := Tree{PID: 10}
	for tick := 0; tick < 5; tick++ {
		at := start.Add(time.Duration(tick) * time.Second)
		snapshot := fixtureSnapshot(at, fixtureProcess(10, 1, 10, time.Duration(tick)*time.Second, 0, 4096, at))
		if tick == 2 {
			snapshot.Available = false
		}
		reading := tree.Observe(snapshot)
		if tick == 0 || tick == 2 || tick == 3 {
			checkCPUTiming(t, reading, nil)
			if reading.CPUQuality != "unavailable" {
				t.Fatalf("tick %d supplied usable CPU: %+v", tick, reading)
			}
			continue
		}
		checkCPUTiming(t, reading, &referenceCPUTiming{
			PreviousScan: referenceScan{Start: at.Add(-time.Second), End: at.Add(-time.Second)},
			CurrentScan:  referenceScan{Start: at, End: at},
		})
	}
}

func TestTreeRejectsOverlappingCPUScansWithoutLosingMemory(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tree := Tree{PID: 10}
	first := fixtureSnapshot(start, fixtureProcess(10, 1, 10, 0, 0, 4096, start.Add(time.Second)))
	first.End = start.Add(time.Second)
	tree.Observe(first)
	second := fixtureSnapshot(start.Add(500*time.Millisecond), fixtureProcess(10, 1, 10, time.Second, 0, 8192, start.Add(2*time.Second)))
	second.End = start.Add(2 * time.Second)
	got := tree.Observe(second)
	checkCPUTiming(t, got, nil)
	if got.CPUQuality != "unavailable" || got.MemoryQuality != "sampled" || got.RSSBytes != 8192 {
		t.Fatalf("invalid timing qualified CPU or discarded independent memory: %+v", got)
	}
}
