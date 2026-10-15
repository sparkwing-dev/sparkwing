package nodehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: an earlier host of the retry copied the generator and stopped before its nodes were added.
func TestAResumedRetryRegeneratesTheGeneratorItAdopts(t *testing.T) {
	st := openStore(t)
	sourceRun(t, st, map[string]string{"targets": "success", "lint": "success", "test-a": "failed", "summary": "cancelled"})
	ctx := t.Context()
	if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	doc := fanOutDoc()
	if err := st.CreateNode(ctx, pendingRow(&Run{ID: testRunID, Doc: doc}, doc.Nodes[0])); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "targets", "success", "", []byte(`"from-targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
	var regenerated json.RawMessage
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
		rehydrate: &Rehydration{SourceRunID: sourceRunID}, rerun: map[string]bool{},
		regenerate: func(_ context.Context, n *NodeRun, out json.RawMessage) []expansion {
			regenerated = out
			return []expansion{{closure: testGenerator, nodes: []nodeproto.PlanNode{{ID: "test-a", Deps: deps(n.Plan.ID)}}}}
		},
	}
	if err := s.add(ctx, ctx, doc.Nodes, nil); err != nil {
		t.Fatal(err)
	}
	s.loop(ctx)
	if status, err := s.verdict(ctx); status != "success" || string(regenerated) != `"from-targets"` {
		t.Fatalf("status %s (%v) regenerated from %q: the adopted generator's nodes must be generated again from its output",
			status, err, regenerated)
	}
}

func TestARetryNeverCopiesOverARowItsRunAlreadyHolds(t *testing.T) {
	for _, mode := range []string{"terminal", "unfinished", "claimed"} {
		t.Run(mode, func(t *testing.T) {
			st := openStore(t)
			ctx := t.Context()
			sourceRun(t, st, map[string]string{"a": "success"})
			if mode == "claimed" {
				identity := store.ClaimIdentity{Principal: "runner", TokenPrefix: "swr_probe"}
				if err := st.CreateTrigger(ctx, store.Trigger{ID: testRunID, Pipeline: "p", Status: "pending"}); err != nil {
					t.Fatal(err)
				}
				trig, err := st.ClaimNextTriggerFor(ctx, identity, store.MaxLeaseDuration, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trig.ClaimSeq})
			}
			if err := st.CreateRun(ctx, store.Run{ID: testRunID, Pipeline: "p", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			if err := st.CreateNode(ctx, store.Node{RunID: testRunID, NodeID: "a", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "terminal":
				if err := st.FinishNodeWithReason(ctx, testRunID, "a", "failed", "current failed", nil, "", nil); err != nil {
					t.Fatal(err)
				}
			case "claimed":
				if _, err := st.ClaimNamedNode(ctx, store.ClaimIdentity{}, testRunID, "a", "other", store.MaxLeaseDuration, store.NamedClaimOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
			run := &Run{ID: testRunID, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest)}
			s := &scheduler{
				run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
				rehydrate: &Rehydration{SourceRunID: sourceRunID}, rerun: map[string]bool{},
			}
			if err := s.add(ctx, ctx, []nodeproto.PlanNode{{ID: "a"}}, nil); err != nil {
				t.Fatal(err)
			}
			s.loop(ctx)
			row := nodeRow(t, st, "a")
			switch mode {
			case "terminal":
				if row.Outcome != "failed" || row.Error != "current failed" {
					t.Fatalf("the run's own finished row was overwritten: %+v", row)
				}
			case "unfinished":
				if row.Outcome != "success" {
					t.Fatalf("a pending row was not copied from the source: %+v", row)
				}
			case "claimed":
				// safety: the fake dispatch ignores claims, which a real one honors; what is checked is no copy.
				if got := skippedFromRetry(t, st); len(got) != 0 {
					t.Fatalf("another holder's live row was overwritten with the source's result (%v)", got)
				}
			}
		})
	}
}
