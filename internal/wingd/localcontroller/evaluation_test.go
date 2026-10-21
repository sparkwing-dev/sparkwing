package localcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func FuzzOwnerEvaluationReadsOnlyItsSourceEdges(f *testing.F) {
	for i := uint8(0); i < 12; i++ {
		f.Add(i, "suffix")
	}
	f.Fuzz(func(t *testing.T, mutation uint8, suffix string) {
		row := func(id string, job nodeproto.JobSpec) store.NodeRow {
			raw, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			return store.NodeRow{Ref: controllerwire.NodeRef{RunID: "run", NodeID: runspec.NodeID(id)}, Generation: 3, SpecHash: "source-hash", Spec: raw}
		}
		source := row("reader", nodeproto.JobSpec{ID: "reader", Needs: []string{"needed"}, NeedsIfPresent: []string{"optional"}, OnFailureOf: "failure", NeedsGenerated: []string{"group"}})
		rows := map[string]store.NodeRow{
			"reader":       source,
			"needed":       row("needed", nodeproto.JobSpec{ID: "needed"}),
			"optional":     row("optional", nodeproto.JobSpec{ID: "optional"}),
			"failure":      row("failure", nodeproto.JobSpec{ID: "failure"}),
			"generated":    row("generated", nodeproto.JobSpec{ID: "generated", Groups: []string{"group"}}),
			"unrelated":    row("unrelated", nodeproto.JobSpec{ID: "unrelated"}),
			"other-reader": row("other-reader", nodeproto.JobSpec{ID: "other-reader", Needs: []string{"unrelated"}}),
		}
		target := capability.Target{Team: "team", RunID: "run", NodeID: "needed"}
		allowed := false
		switch mutation % 12 {
		case 0:
			allowed = true
		case 1:
			target.NodeID = "unrelated"
		case 2:
			source.Generation++
		case 3:
			source.SpecHash += "-" + suffix
		case 4:
			source.Ref.NodeID += runspec.NodeID("-" + suffix)
		case 5:
			target.RunID += "-" + suffix
		case 6:
			output := rows["needed"]
			output.Ref.RunID += runspec.RunID("-" + suffix)
			rows["needed"] = output
		case 7:
			target.NodeID = "generated"
			allowed = true
		case 8:
			target.NodeID = "optional"
			allowed = true
		case 9:
			target.NodeID = "failure"
			allowed = true
		case 10:
			output := rows["generated"]
			output.Spec = []byte(`{"id":"generated","groups":["other-group"]}`)
			rows["generated"] = output
			target.NodeID = "generated"
		case 11:
			target.NodeID += "-" + suffix
		}
		rows["reader"] = source
		relations := evaluationBindings{source: controllerwire.NodeRef{RunID: "run", NodeID: "reader"}, generation: 3, hash: "source-hash", readNode: func(_ context.Context, ref controllerwire.NodeRef) (store.NodeRow, error) {
			if ref.RunID != "run" {
				return store.NodeRow{}, store.ErrNotFound
			}
			result, ok := rows[string(ref.NodeID)]
			if !ok {
				return store.NodeRow{}, store.ErrNotFound
			}
			return result, nil
		}}
		principal := capability.Principal{Capability: capability.Capability{Unit: capability.UnitTrigger, Binding: capability.Binding{Kind: capability.BindingTrigger, RunID: "run"}}}
		err := relations.CheckBinding(t.Context(), principal, capability.BindReadSet, target)
		if (err == nil) != allowed {
			t.Fatalf("source mutation %d allowed=%t: %v", mutation%12, allowed, err)
		}
		if !allowed {
			code := apierr.Forbidden
			if mutation%12 >= 2 && mutation%12 <= 4 {
				code = apierr.LockHeld
			}
			var failure *apierr.Error
			if !errors.As(err, &failure) || failure.Code != code {
				t.Fatalf("source mutation %d: expected %s, got %v", mutation%12, code, err)
			}
		}
	})
}
