package orchestrator

import (
	"context"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// printRunSummaries renders the markdown summaries emitted by
// sparkwing.Summary() during the run as a trailing "Summaries"
// section. Mirrors the visual structure of the renderer's Summary
// block: a labeled rule, indented body, closing rule.
//
// Step-scoped summaries render under "<node> › <step>:" headers and
// node-scoped summaries under "<node>:". The section is skipped
// entirely when no summaries were emitted -- runs that don't use
// Summary() see no change to the output.
//
// Called from the pipeline-binary main after RunLocal returns so the
// section appears below the renderer's run-summary block.
func printRunSummaries(ctx context.Context, w io.Writer, useColor bool, st *store.Store, runID string) error {
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	steps, err := st.ListNodeSteps(ctx, runID)
	if err != nil {
		return err
	}
	type entry struct {
		nodeID string
		stepID string
		md     string
	}
	var entries []entry
	for _, n := range nodes {
		if n.Summary != "" {
			entries = append(entries, entry{nodeID: n.NodeID, md: n.Summary})
		}
	}
	for _, s := range steps {
		if s.Summary != "" {
			entries = append(entries, entry{nodeID: s.NodeID, stepID: s.StepID, md: s.Summary})
		}
	}
	if len(entries) == 0 {
		return nil
	}

	// Borrow PrettyRenderer for its section rule + palette so the
	// trailing section visually matches the renderer's Summary block.
	r := NewPrettyRendererTo(w, useColor)
	fmt.Fprintln(w)
	fmt.Fprintln(w, r.sectionRule("Summaries"))
	for i, e := range entries {
		if i > 0 {
			fmt.Fprintln(w)
		}
		hue := r.hueFor(e.nodeID)
		header := r.color(e.nodeID, ansiBold+hue)
		if e.stepID != "" {
			header += r.color(" › ", ansiDim) + r.color(e.stepID, ansiBold)
		}
		fmt.Fprintln(w, "  "+header)
		renderMarkdownSummary(w, "    ", e.md)
	}
	fmt.Fprintln(w, r.sectionRule(""))
	return nil
}
