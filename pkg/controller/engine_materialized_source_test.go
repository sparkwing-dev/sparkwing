package controller

import (
	"reflect"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func FuzzEngineMaterializedSourcePreservesIdentity(f *testing.F) {
	f.Add("memo-run", "generated/build", int64(9007199254740993))
	f.Add("r\"雪", "a:b@c", int64(1))
	f.Fuzz(func(t *testing.T, run, job string, seq int64) {
		if !utf8.ValidString(run) || !utf8.ValidString(job) {
			t.Skip()
		}
		source := controllerwire.ReuseSource{Policy: "manual_retry", Result: &controllerwire.MaterializedResultSource{RunID: run, JobID: job, StateSeq: seq}}
		stored := reconcileSourceForStore(source)
		if stored.Result == nil || stored.Result.Ref.RunID != run || stored.Result.Ref.NodeID != job || stored.Result.StateSeq != seq || stored.Source.Attempt != 0 {
			t.Fatalf("store projection lost materialized identity: %+v", stored)
		}
		if got := engineReuseSource(stored); !reflect.DeepEqual(got, source) {
			t.Fatalf("source projection changed: %+v, want %+v", got, source)
		}
	})
}

func TestEngineAttemptSourceRemainsAnAttempt(t *testing.T) {
	source := controllerwire.ReuseSource{Policy: "resume"}
	source.Attempt.RunID, source.Attempt.NodeID = "run", "job"
	source.Attempt.Attempt, source.Attempt.Generation = 4, 2
	stored := reconcileSourceForStore(source)
	if stored.Result != nil || stored.Source != source.Attempt || !reflect.DeepEqual(engineReuseSource(stored), source) {
		t.Fatal("attempt source acquired an invented materialized result")
	}
}

func FuzzEngineSnapshotPreservesGuardVerdict(f *testing.F) {
	f.Add("branch refused", false, false)
	f.Add("", false, true)
	f.Add("not a refusal", true, false)
	f.Fuzz(func(t *testing.T, reason string, passed, cancel bool) {
		snapshot := store.RunSnapshot{Run: store.RunRow{ID: "run", Plan: []byte(`{"jobs":[]}`), Spec: &runspec.Spec{Resolved: runspec.Resolved{Guards: runspec.Guards{When: []string{"where:local"}, Reason: reason, Passed: passed}}}}}
		if cancel {
			now := time.Unix(1, 0)
			snapshot.Run.CancelRequestedAt = &now
		}
		got, err := engineSchedule(snapshot)
		if err != nil || got.CancelRequested != cancel || (got.GuardRefusal == "") != passed {
			t.Fatalf("guard verdict changed: %+v, %v", got, err)
		}
		if !passed && reason != "" && got.GuardRefusal != reason {
			t.Fatal("guard reason changed")
		}
	})
}
