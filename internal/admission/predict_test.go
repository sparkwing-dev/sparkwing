package admission

import (
	"math"
	"reflect"
	"testing"
)

func TestPredictionUsesBackfillPolicyAndLeavesSnapshotUntouched(t *testing.T) {
	for _, flexible := range []bool{false, true} {
		t.Run(map[bool]string{false: "strict", true: "flexible"}[flexible], func(t *testing.T) {
			cfg := Config{TotalMemoryBytes: 8 << 30}
			if flexible {
				cfg.Scheduling = AutoPolicy()
			}
			l, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			older := mustGrant(t, l, Request{ID: "older", MemoryBytes: 6 << 30})
			mustGrant(t, l, Request{ID: "guard", MemoryBytes: 1 << 30})
			if _, err := l.SetHeadroom(0, 5<<30); err != nil {
				t.Fatal(err)
			}
			mustQueue(t, l, Request{ID: "heavy", MemoryBytes: 6 << 30})
			mustQueue(t, l, Request{ID: "light", MemoryBytes: 4 << 30, ExpectedP99MS: 1000})
			snap := l.Snapshot()
			starts, _, err := Predict(snap, map[string]float64{"older": 1000, "guard": 10000, "heavy": 2000, "light": 1000})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(snap, l.Snapshot()) {
				t.Fatal("prediction mutated its source")
			}
			events := mustRelease(t, l, older.ID, "older")
			promoted := false
			for _, event := range events {
				if event.Kind == EventPromoted && event.RequestID == "light" {
					promoted = true
				}
			}
			if promoted != flexible || (starts["light"] == 1000) != promoted {
				t.Fatalf("light starts at %v, live promotion %v, flexible %v", starts["light"], promoted, flexible)
			}
		})
	}
}

func TestPredictionUsesSoftCPUAndSemaphoreCapacity(t *testing.T) {
	for _, soft := range []bool{false, true} {
		l := testLedger(t, 10, 0)
		mustGrant(t, l, Request{ID: "cpu", Cores: 6})
		semHolder := mustGrant(t, l, Request{ID: "sem", Semaphores: []SemaphoreClaim{sem("k", 1, 1, PolicyQueue)}})
		mustQueue(t, l, Request{ID: "waiter", Cores: 6, SoftCores: soft, Semaphores: []SemaphoreClaim{sem("k", 2, 1, PolicyQueue)}})
		starts, _, err := Predict(l.Snapshot(), map[string]float64{"cpu": 10000, "sem": 1000, "waiter": 1000})
		if err != nil {
			t.Fatal(err)
		}
		events := mustRelease(t, l, semHolder.ID, "sem")
		promoted := false
		for _, event := range events {
			if event.Kind == EventPromoted {
				promoted = true
			}
		}
		want := float64(10000)
		if soft {
			want = 1000
		}
		if starts["waiter"] != want || promoted != soft {
			t.Fatalf("soft %v: start %v, promoted %v", soft, starts, promoted)
		}
	}
}

func TestPredictionKeepsUnknownReleasesUnknown(t *testing.T) {
	l := testLedger(t, 1, 0)
	mustGrant(t, l, Request{ID: "holder", Cores: 1})
	mustQueue(t, l, Request{ID: "waiter", Cores: 1})
	starts, clear, err := Predict(l.Snapshot(), nil)
	if err != nil || !math.IsInf(starts["waiter"], 1) || !math.IsInf(clear, 1) {
		t.Fatalf("prediction = %v %v %v", starts, clear, err)
	}
}
