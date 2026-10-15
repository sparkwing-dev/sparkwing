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
	if err := st.CreateNode(ctx, pendingRow(&Run{ID: testRunID, Doc: doc, validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}, doc.Jobs[0])); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishNodeWithReason(ctx, testRunID, "targets", "success", "", []byte(`"from-targets"`), "", nil); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dispatch: remoteHost(st, sparkwing.Success, "", nil)}
	run := &Run{ID: testRunID, Doc: doc, State: st, Logger: slog.New(slog.DiscardHandler), adds: make(chan addRequest), validation: nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}}
	var regenerated json.RawMessage
	s := &scheduler{
		run: run, results: map[string]Result{}, started: map[string]bool{}, exec: chain(&cfg), resume: true,
		rehydrate: &Rehydration{SourceRunID: sourceRunID}, rerun: map[string]bool{},
		regenerate: func(_ context.Context, n *NodeRun, out json.RawMessage) []expansion {
			regenerated = out
			return []expansion{{closure: testGenerator, nodes: []nodeproto.JobSpec{{ID: "test-a", Needs: deps(n.Plan.ID), Steps: []nodeproto.StepSpec{{ID: "body"}}}}}}
		},
	}
	if err := s.add(ctx, ctx, doc.Jobs, nil); err != nil {
		t.Fatal(err)
	}
	s.loop(ctx)
	if status, err := s.verdict(ctx); status != "success" || string(regenerated) != `"from-targets"` {
		t.Fatalf("status %s (%v) regenerated from %q: the adopted generator's nodes must be generated again from its output",
			status, err, regenerated)
	}
}
