// Handlers for the `sparkwing runs annotations` verbs: list + add.
//
// Annotations are persistent summary strings the SDK (sparkwing.Annotate)
// or external agents append to a node (or step) during a run. They show
// up on the dashboard alongside outcome and are surfaced here so agents
// can read or write them via the CLI without poking at the store.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// annotationEntry is the wire shape emitted by `runs annotations list`.
// One entry per appended string; step is empty when the annotation
// lives on the node itself rather than an inner step.
type annotationEntry struct {
	RunID   string `json:"run_id"`
	NodeID  string `json:"node_id"`
	StepID  string `json:"step_id,omitempty"`
	Message string `json:"message"`
}

// runRunsAnnotations routes the `sparkwing runs annotations` subverb.
func runRunsAnnotations(ctx context.Context, paths orchestrator.Paths, args []string) error {
	if handleParentHelp(cmdAnnotations, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdAnnotations, os.Stdout)
		return nil
	}
	switch args[0] {
	case "list":
		return runAnnotationsList(ctx, paths, args[1:])
	case "add":
		return runAnnotationsAdd(ctx, paths, args[1:])
	default:
		PrintHelp(cmdAnnotations, os.Stderr)
		return fmt.Errorf("runs annotations: unknown subcommand %q", args[0])
	}
}

// runAnnotationsList implements `sparkwing runs annotations list`.
func runAnnotationsList(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdAnnotationsList.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier (required)")
	nodeID := fs.String("node", "", "limit to one node id")
	stepID := fs.String("step", "", "limit to one step id (implies node-scope or step-scope reads)")
	includeSteps := fs.Bool("steps", false, "include per-step annotations as separate rows")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	on := fs.String("on", "", "profile name; omit for local-only")
	if err := parseAndCheck(cmdAnnotationsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *runID == "" {
		return fmt.Errorf("%s: --run is required", cmdAnnotationsList.Path)
	}
	resolvedFmt, err := resolveOutputFormat(*outFmt, fs.Changed("output"), false, cmdAnnotationsList.Path)
	if err != nil {
		return err
	}
	rid := normalizeRunID(*runID)

	var entries []annotationEntry
	if *on == "" {
		entries, err = listLocalAnnotations(ctx, paths, rid, *nodeID, *stepID, *includeSteps || *stepID != "")
	} else {
		entries, err = listRemoteAnnotations(ctx, *on, rid, *nodeID, *stepID, *includeSteps || *stepID != "")
	}
	if err != nil {
		return err
	}

	switch resolvedFmt {
	case "json":
		if entries == nil {
			entries = []annotationEntry{}
		}
		return writeAnnotationsJSON(os.Stdout, entries)
	case "plain":
		for _, e := range entries {
			if e.StepID != "" {
				fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", e.NodeID, e.StepID, e.Message)
			} else {
				fmt.Fprintf(os.Stdout, "%s\t%s\n", e.NodeID, e.Message)
			}
		}
		return nil
	default:
		return renderAnnotationsTable(os.Stdout, entries)
	}
}

func listLocalAnnotations(ctx context.Context, paths orchestrator.Paths, runID, nodeFilter, stepFilter string, includeSteps bool) ([]annotationEntry, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, err
	}
	defer st.Close()

	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []annotationEntry
	if stepFilter == "" {
		for _, n := range nodes {
			if nodeFilter != "" && n.NodeID != nodeFilter {
				continue
			}
			for _, msg := range n.Annotations {
				out = append(out, annotationEntry{RunID: runID, NodeID: n.NodeID, Message: msg})
			}
		}
	}
	if includeSteps {
		steps, err := st.ListNodeSteps(ctx, runID)
		if err != nil {
			return nil, err
		}
		for _, s := range steps {
			if nodeFilter != "" && s.NodeID != nodeFilter {
				continue
			}
			if stepFilter != "" && s.StepID != stepFilter {
				continue
			}
			for _, msg := range s.Annotations {
				out = append(out, annotationEntry{
					RunID: runID, NodeID: s.NodeID, StepID: s.StepID, Message: msg,
				})
			}
		}
	}
	return out, nil
}

func listRemoteAnnotations(ctx context.Context, profileName, runID, nodeFilter, stepFilter string, includeSteps bool) ([]annotationEntry, error) {
	prof, err := resolveProfile(profileName)
	if err != nil {
		return nil, err
	}
	if err := requireController(prof, cmdAnnotationsList.Path); err != nil {
		return nil, err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	var out []annotationEntry
	if stepFilter == "" {
		nodes, err := c.ListNodes(ctx, runID)
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			if nodeFilter != "" && n.NodeID != nodeFilter {
				continue
			}
			for _, msg := range n.Annotations {
				out = append(out, annotationEntry{RunID: runID, NodeID: n.NodeID, Message: msg})
			}
		}
	}
	if includeSteps {
		steps, err := c.ListNodeSteps(ctx, runID)
		if err != nil {
			return nil, err
		}
		for _, s := range steps {
			if nodeFilter != "" && s.NodeID != nodeFilter {
				continue
			}
			if stepFilter != "" && s.StepID != stepFilter {
				continue
			}
			for _, msg := range s.Annotations {
				out = append(out, annotationEntry{
					RunID: runID, NodeID: s.NodeID, StepID: s.StepID, Message: msg,
				})
			}
		}
	}
	return out, nil
}

func renderAnnotationsTable(w io.Writer, entries []annotationEntry) error {
	if len(entries) == 0 {
		fmt.Fprintln(w, "no annotations on this run")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tSTEP\tMESSAGE")
	for _, e := range entries {
		step := e.StepID
		if step == "" {
			step = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", e.NodeID, step, e.Message)
	}
	return tw.Flush()
}

func writeAnnotationsJSON(w io.Writer, entries []annotationEntry) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// runAnnotationsAdd implements `sparkwing runs annotations add`.
func runAnnotationsAdd(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdAnnotationsAdd.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier (required)")
	nodeID := fs.String("node", "", "node identifier (required)")
	stepID := fs.String("step", "", "step identifier (optional; annotates the step instead of the node)")
	msg := fs.StringP("message", "m", "", "annotation text (required)")
	on := fs.String("on", "", "profile name; omit for local-only")
	if err := parseAndCheck(cmdAnnotationsAdd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *runID == "" || *nodeID == "" {
		return fmt.Errorf("%s: --run and --node are required", cmdAnnotationsAdd.Path)
	}
	if *msg == "" {
		return fmt.Errorf("%s: --message is required", cmdAnnotationsAdd.Path)
	}
	rid := normalizeRunID(*runID)

	if *on == "" {
		return addLocalAnnotation(ctx, paths, rid, *nodeID, *stepID, *msg)
	}
	return addRemoteAnnotation(ctx, *on, rid, *nodeID, *stepID, *msg)
}

func addLocalAnnotation(ctx context.Context, paths orchestrator.Paths, runID, nodeID, stepID, msg string) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	if stepID != "" {
		if err := st.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "annotated %s/%s/%s: %s\n", runID, nodeID, stepID, msg)
		return nil
	}
	if err := st.AppendNodeAnnotation(ctx, runID, nodeID, msg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "annotated %s/%s: %s\n", runID, nodeID, msg)
	return nil
}

func addRemoteAnnotation(ctx context.Context, profileName, runID, nodeID, stepID, msg string) error {
	prof, err := resolveProfile(profileName)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdAnnotationsAdd.Path); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	if stepID != "" {
		if err := c.AppendStepAnnotation(ctx, runID, nodeID, stepID, msg); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "annotated %s/%s/%s: %s\n", runID, nodeID, stepID, msg)
		return nil
	}
	if err := c.AppendNodeAnnotation(ctx, runID, nodeID, msg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "annotated %s/%s: %s\n", runID, nodeID, msg)
	return nil
}
