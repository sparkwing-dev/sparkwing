package nodehost

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRetryExecutesCommittedSelectionAfterRestart(t *testing.T) {
	for _, scenario := range []struct {
		name, mode  string
		lateSuccess bool
	}{{"failed", "failed", false}, {"all", "all", false}, {"later_success", "failed", true}} {
		t.Run(scenario.name, func(t *testing.T) {
			mode := scenario.mode
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			original := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
				{ID: "setup", Steps: []nodeproto.StepSpec{{ID: "body"}}},
				{ID: "broken", Needs: []string{"setup"}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
			}})
			original.Store.SetResultBlobRecords(nil)
			original.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				outcome := store.NodeSuccess
				if input.NodeID == "broken" {
					outcome = store.NodeFailed
				}
				return original.report(ctx, input, outcome)
			})
			if scenario.lateSuccess {
				entry, err := original.Host.Acquire(ctx, original.Input)
				if err != nil {
					t.Fatal(err)
				}
				if err := original.Host.execute(ctx, original.Input, original.Accepted, entry, store.ReconcileRequest{Ref: store.NodeRef{RunID: original.Input.RunID, NodeID: "setup"}, Actor: original.Actor, Policy: store.SourceNone}); err != nil {
					t.Fatal(err)
				}
			} else if result, err := original.Host.Run(ctx, original.Input); err != nil || result.Outcome != "failed" {
				t.Fatalf("source completion=%+v error=%v", result, err)
			}
			source, err := original.Tenant.ReadRun(ctx, original.Input.RunID)
			if err != nil {
				t.Fatal(err)
			}
			retry := newStoreOwnerIntakeFixture(t, original.Store, store.IntakeRequest{
				RetryMode: mode, Request: runspec.Request{Lineage: runspec.Lineage{RetryOf: source.ID}},
			})
			if scenario.lateSuccess {
				original.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
					return original.report(ctx, input, store.NodeSuccess)
				})
				if result, err := original.Host.Run(ctx, original.Input); err != nil || result.Outcome != "success" {
					t.Fatalf("late source completion=%+v error=%v", result, err)
				}
			}
			retry.Host.Acquire = original.Host.Acquire
			executed := make(chan string, 2)
			retry.Host.Executor = ownerNodeFunc(func(ctx context.Context, input NodeInput) error {
				executed <- input.NodeID
				return retry.report(ctx, input, store.NodeSuccess)
			})
			if result, err := retry.Host.Run(ctx, retry.Input); err != nil || result.Outcome != "success" {
				t.Fatalf("retry completion=%+v error=%v", result, err)
			}
			close(executed)
			var got []string
			for id := range executed {
				got = append(got, id)
			}
			want := []string{"broken"}
			if mode == "all" {
				want = []string{"setup", "broken"}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s executed %v, want %v", mode, got, want)
			}
			setup, err := retry.Tenant.ReadNode(ctx, store.NodeRef{RunID: retry.Input.RunID, NodeID: "setup"})
			if err != nil || mode == "failed" && setup.Attempt != 0 || mode == "all" && setup.Attempt != 1 {
				t.Fatalf("retry execution/materialization attempts=%d error=%v", setup.Attempt, err)
			}
			stored, err := retry.Tenant.ReadRun(ctx, retry.Input.RunID)
			if err != nil || !reflect.DeepEqual(stored.Request, source.Request) || !reflect.DeepEqual(stored.Spec, source.Spec) {
				t.Fatalf("selection changed seals: %+v %v", stored, err)
			}
		})
	}
}
