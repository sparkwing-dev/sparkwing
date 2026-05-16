// `sparkwing runs summary` -- run-level aggregated work view across
// every node. Mirrors the dashboard's Summary tab so an agent
// auditing a run sees groups, work items, modifiers, annotations,
// and approvals in one render instead of paging through nodes.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
)

// SummaryOpts configures `runs summary`.
type SummaryOpts struct {
	JSON bool
}

// SummaryAnnotation is one annotation in the run-wide list.
type SummaryAnnotation struct {
	NodeID  string `json:"node_id"`
	StepID  string `json:"step_id,omitempty"`
	Message string `json:"message"`
}

// SummaryGroup is one node group (members are node ids).
type SummaryGroup struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// SummaryWorkItem aggregates the same step id across nodes.
type SummaryWorkItem struct {
	NodeID   string   `json:"node_id"`
	StepID   string   `json:"step_id,omitempty"`
	Status   string   `json:"status,omitempty"`
	Duration string   `json:"duration,omitempty"`
	IsNode   bool     `json:"is_node,omitempty"`
	Needs    []string `json:"needs,omitempty"`
}

// SummaryModifier is one node-level modifier in effect. Aggregated
// across the run so the rollup answers "what modifiers did this run
// actually exercise".
type SummaryModifier struct {
	Modifier string   `json:"modifier"`
	Nodes    []string `json:"nodes"`
}

// RunSummary is the wire shape of `runs summary -o json`.
type RunSummary struct {
	RunID       string              `json:"run_id"`
	Pipeline    string              `json:"pipeline"`
	Status      string              `json:"status"`
	Trigger     string              `json:"trigger,omitempty"`
	StartedAt   time.Time           `json:"started_at"`
	FinishedAt  *time.Time          `json:"finished_at,omitempty"`
	DurationMS  int64               `json:"duration_ms,omitempty"`
	Error       string              `json:"error,omitempty"`
	Annotations []SummaryAnnotation `json:"annotations,omitempty"`
	Groups      []SummaryGroup      `json:"groups,omitempty"`
	WorkItems   []SummaryWorkItem   `json:"work_items,omitempty"`
	Modifiers   []SummaryModifier   `json:"modifiers,omitempty"`
	Approvals   []*store.Approval   `json:"approvals,omitempty"`
}

// RunSummaryLocal opens the local store and renders the summary.
func RunSummaryLocal(ctx context.Context, paths Paths, runID string, opts SummaryOpts, out io.Writer) error {
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	steps, _ := st.ListNodeSteps(ctx, runID)
	approvals, _ := st.ListApprovalsForRun(ctx, runID)
	return renderSummary(buildSummary(run, nodes, steps, approvals), opts, out)
}

// RunSummaryRemote is the cluster-mode counterpart.
func RunSummaryRemote(ctx context.Context, controllerURL, token, runID string, opts SummaryOpts, out io.Writer) error {
	if controllerURL == "" {
		return errors.New("RunSummaryRemote: controller URL required")
	}
	c := client.NewWithToken(controllerURL, nil, token)
	run, err := c.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	nodes, err := c.ListNodes(ctx, runID)
	if err != nil {
		return err
	}
	steps, _ := c.ListNodeSteps(ctx, runID)
	approvals, _ := c.ListApprovalsForRun(ctx, runID)
	return renderSummary(buildSummary(run, nodes, steps, approvals), opts, out)
}

// buildSummary assembles the RunSummary from store rows + the
// plan snapshot's decorations. Pure: no I/O, safe to unit-test.
func buildSummary(run *store.Run, nodes []*store.Node, steps []*store.NodeStep, approvals []*store.Approval) RunSummary {
	s := RunSummary{
		RunID:     run.ID,
		Pipeline:  run.Pipeline,
		Status:    run.Status,
		Trigger:   run.TriggerSource,
		StartedAt: run.StartedAt,
		Error:     run.Error,
		Approvals: approvals,
	}
	if run.FinishedAt != nil {
		s.FinishedAt = run.FinishedAt
		s.DurationMS = run.FinishedAt.Sub(run.StartedAt).Milliseconds()
	}

	for _, n := range nodes {
		for _, msg := range n.Annotations {
			s.Annotations = append(s.Annotations, SummaryAnnotation{NodeID: n.NodeID, Message: msg})
		}
	}
	for _, st := range steps {
		for _, msg := range st.Annotations {
			s.Annotations = append(s.Annotations, SummaryAnnotation{NodeID: st.NodeID, StepID: st.StepID, Message: msg})
		}
	}

	dmap := api.DecorationsFromSnapshot(run.PlanSnapshot)
	s.Groups = aggregateGroups(nodes, dmap)
	s.Modifiers = aggregateModifiers(nodes, dmap)
	s.WorkItems = aggregateWorkItems(nodes, steps)
	return s
}

func aggregateGroups(nodes []*store.Node, dmap map[string]*api.Decorations) []SummaryGroup {
	idx := map[string][]string{}
	for _, n := range nodes {
		dec := dmap[n.NodeID]
		if dec == nil {
			continue
		}
		for _, g := range dec.Groups {
			idx[g] = append(idx[g], n.NodeID)
		}
	}
	out := make([]SummaryGroup, 0, len(idx))
	for name, members := range idx {
		out = append(out, SummaryGroup{Name: name, Members: members})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func aggregateModifiers(nodes []*store.Node, dmap map[string]*api.Decorations) []SummaryModifier {
	idx := map[string][]string{}
	add := func(name, node string) {
		idx[name] = append(idx[name], node)
	}
	for _, n := range nodes {
		dec := dmap[n.NodeID]
		if dec == nil {
			continue
		}
		if dec.Approval {
			add("Approval", n.NodeID)
		}
		if dec.Dynamic {
			add("Dynamic", n.NodeID)
		}
		if len(dec.SpawnedPipelines) > 0 {
			add("RunAndAwait", n.NodeID)
		}
		m := dec.Modifiers
		if m == nil {
			continue
		}
		if m.Inline {
			add("Inline", n.NodeID)
		}
		if m.Retry > 0 {
			add("Retry", n.NodeID)
		}
		if m.TimeoutMS > 0 {
			add("Timeout", n.NodeID)
		}
		if len(m.RunsOn) > 0 {
			add("Requires", n.NodeID)
		}
		if m.CacheKey != "" {
			add("Cache", n.NodeID)
		}
		if m.HasBeforeRun {
			add("BeforeRun", n.NodeID)
		}
		if m.HasAfterRun {
			add("AfterRun", n.NodeID)
		}
		if m.HasSkipIf {
			add("SkipIf", n.NodeID)
		}
		if m.OnFailure != "" {
			add("OnFailure", n.NodeID)
		}
		if m.ContinueOnError {
			add("ContinueOnError", n.NodeID)
		}
	}
	out := make([]SummaryModifier, 0, len(idx))
	for name, nodes := range idx {
		out = append(out, SummaryModifier{Modifier: name, Nodes: nodes})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modifier < out[j].Modifier })
	return out
}

func aggregateWorkItems(nodes []*store.Node, steps []*store.NodeStep) []SummaryWorkItem {
	byNode := map[string][]*store.NodeStep{}
	for _, s := range steps {
		byNode[s.NodeID] = append(byNode[s.NodeID], s)
	}
	var out []SummaryWorkItem
	for _, n := range nodes {
		row := SummaryWorkItem{NodeID: n.NodeID, Status: n.Status, IsNode: true, Needs: n.Deps}
		if n.StartedAt != nil && n.FinishedAt != nil {
			row.Duration = n.FinishedAt.Sub(*n.StartedAt).Round(time.Millisecond).String()
		}
		out = append(out, row)
		for _, s := range byNode[n.NodeID] {
			si := SummaryWorkItem{NodeID: n.NodeID, StepID: s.StepID, Status: s.Status}
			if s.StartedAt != nil && s.FinishedAt != nil {
				si.Duration = s.FinishedAt.Sub(*s.StartedAt).Round(time.Millisecond).String()
			}
			out = append(out, si)
		}
	}
	return out
}

// renderSummary emits the text or JSON form of s.
func renderSummary(s RunSummary, opts SummaryOpts, out io.Writer) error {
	if opts.JSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(s)
	}
	fmt.Fprintf(out, "run:       %s\n", s.RunID)
	fmt.Fprintf(out, "pipeline:  %s\n", s.Pipeline)
	fmt.Fprintf(out, "status:    %s\n", s.Status)
	if s.Trigger != "" {
		fmt.Fprintf(out, "trigger:   %s\n", s.Trigger)
	}
	fmt.Fprintf(out, "started:   %s\n", s.StartedAt.Local().Format("2006-01-02 15:04:05"))
	if s.FinishedAt != nil {
		fmt.Fprintf(out, "finished:  %s  (duration %s)\n",
			s.FinishedAt.Local().Format("2006-01-02 15:04:05"),
			time.Duration(s.DurationMS)*time.Millisecond)
	}
	if s.Error != "" {
		fmt.Fprintf(out, "error:     %s\n", s.Error)
	}
	if len(s.Annotations) > 0 {
		fmt.Fprintln(out, "\nannotations:")
		for _, a := range s.Annotations {
			if a.StepID != "" {
				fmt.Fprintf(out, "  %s/%s: %s\n", a.NodeID, a.StepID, a.Message)
			} else {
				fmt.Fprintf(out, "  %s: %s\n", a.NodeID, a.Message)
			}
		}
	}
	if len(s.Groups) > 0 {
		fmt.Fprintln(out, "\ngroups:")
		for _, g := range s.Groups {
			fmt.Fprintf(out, "  %s: %s\n", g.Name, strings.Join(g.Members, ", "))
		}
	}
	if len(s.Modifiers) > 0 {
		fmt.Fprintln(out, "\nmodifiers:")
		for _, m := range s.Modifiers {
			fmt.Fprintf(out, "  %s: %s\n", m.Modifier, strings.Join(m.Nodes, ", "))
		}
	}
	if len(s.WorkItems) > 0 {
		fmt.Fprintln(out, "\nwork:")
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  ID\tSTATUS\tDURATION")
		for _, w := range s.WorkItems {
			label := w.NodeID
			if w.StepID != "" {
				label = "  ↳ " + w.NodeID + "/" + w.StepID
			}
			dur := w.Duration
			if dur == "" {
				dur = "-"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", label, w.Status, dur)
		}
		_ = tw.Flush()
	}
	if len(s.Approvals) > 0 {
		renderApprovalsSection(out, s.Approvals)
	}
	return nil
}
