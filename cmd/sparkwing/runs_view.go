package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var runViewNames = []string{"summary", "timeline", "receipt", "errors", "tree"}

type runView struct {
	name   string
	runID  string
	remote *profile.Profile
	format string
	steps  bool
	width  int
}

func renderRunView(ctx context.Context, paths orchestrator.Paths, v runView) error {
	jsonOut := v.format == "json"
	switch v.name {
	case "summary":
		opts := orchestrator.SummaryOpts{JSON: jsonOut}
		if v.remote == nil {
			return orchestrator.RunSummaryLocal(ctx, paths, v.runID, opts, os.Stdout)
		}
		return orchestrator.RunSummaryRemote(ctx, v.remote.ControllerURL(), v.remote.ControllerToken(), v.runID, opts, os.Stdout)
	case "timeline":
		opts := orchestrator.TimelineOpts{Width: v.width, IncludeSteps: v.steps, JSON: jsonOut}
		if v.remote == nil {
			return orchestrator.RunTimeline(ctx, paths, v.runID, opts, os.Stdout)
		}
		return orchestrator.RunTimelineRemote(ctx, v.remote.ControllerURL(), v.remote.ControllerToken(), v.runID, opts, os.Stdout)
	case "receipt":
		return renderRunReceipt(ctx, paths, v)
	case "errors":
		if v.remote == nil {
			return orchestrator.JobErrors(ctx, paths, v.runID, jsonOut, os.Stdout)
		}
		return orchestrator.JobErrorsRemote(ctx, v.remote.ControllerURL(), v.remote.ControllerToken(), v.runID, jsonOut, os.Stdout)
	case "tree":
		return renderRunTree(ctx, paths, v)
	default:
		return fmt.Errorf("runs status: --view must be one of %s, got %q", strings.Join(runViewNames, "|"), v.name)
	}
}

func renderRunReceipt(ctx context.Context, paths orchestrator.Paths, v runView) error {
	if v.remote != nil {
		c := client.NewWithToken(v.remote.ControllerURL(), nil, v.remote.ControllerToken())
		body, err := c.GetRunReceipt(ctx, v.runID)
		if err != nil {
			return err
		}
		var decoded any
		if err := json.Unmarshal(body, &decoded); err != nil {
			return fmt.Errorf("decode receipt: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(decoded)
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, label, done, err := orchestrator.OpenStoreForRun(ctx, paths, v.runID)
	if err != nil {
		return err
	}
	defer done()
	run, err := st.GetRun(ctx, v.runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, v.runID)
	if err != nil {
		return err
	}
	rec := receipt.BuildReceipt(run, nodes, 0, "local (rate not configured)")
	rec.Store = label
	return json.NewEncoder(os.Stdout).Encode(rec)
}

type runTreeNode struct {
	Run      *store.Run     `json:"run"`
	Children []*runTreeNode `json:"children,omitempty"`
}

func renderRunTree(ctx context.Context, paths orchestrator.Paths, v runView) error {
	var fetchChildren func(parentID string) ([]*store.Run, error)
	var root *store.Run
	if v.remote != nil {
		c := client.NewWithToken(v.remote.ControllerURL(), nil, v.remote.ControllerToken())
		r, err := c.GetRun(ctx, v.runID)
		if err != nil {
			return err
		}
		root = r
		fetchChildren = func(parentID string) ([]*store.Run, error) {
			return c.ListRuns(ctx, store.RunFilter{ParentRunID: parentID, Limit: 1000})
		}
	} else {
		if err := paths.EnsureRoot(); err != nil {
			return err
		}
		st, _, done, err := orchestrator.OpenStoreForRun(ctx, paths, v.runID)
		if err != nil {
			return err
		}
		defer done()
		r, err := st.GetRun(ctx, v.runID)
		if err != nil {
			return err
		}
		root = r
		fetchChildren = func(parentID string) ([]*store.Run, error) {
			return st.ListRuns(ctx, store.RunFilter{ParentRunID: parentID, Limit: 1000})
		}
	}

	var build func(r *store.Run) (*runTreeNode, error)
	build = func(r *store.Run) (*runTreeNode, error) {
		node := &runTreeNode{Run: store.RedactedRun(r)}
		kids, err := fetchChildren(r.ID)
		if err != nil {
			return nil, err
		}
		for _, k := range kids {
			child, err := build(k)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, child)
		}
		return node, nil
	}
	tree, err := build(root)
	if err != nil {
		return err
	}
	if v.format == "json" {
		return jsonEncode(os.Stdout, tree)
	}
	var render func(n *runTreeNode, prefix string, last bool)
	render = func(n *runTreeNode, prefix string, last bool) {
		connector := "├── "
		if last {
			connector = "└── "
		}
		if prefix == "" {
			fmt.Printf("%s  %s  %s  (%s)\n", n.Run.ID, n.Run.Pipeline, n.Run.Status, relTime(n.Run.StartedAt))
		} else {
			fmt.Printf("%s%s%s  %s  %s  (%s)\n", prefix, connector, n.Run.ID, n.Run.Pipeline, n.Run.Status, relTime(n.Run.StartedAt))
		}
		for i, c := range n.Children {
			var next string
			switch {
			case prefix == "":
				next = "    "
			case last:
				next = prefix + "    "
			default:
				next = prefix + "│   "
			}
			render(c, next, i == len(n.Children)-1)
		}
	}
	render(tree, "", true)
	return nil
}

// safety: a lookup failure exits 3 and an elapsed timeout exits 2; scripts
// that wait on a run branch on those codes.
func waitForTerminalRun(ctx context.Context, paths orchestrator.Paths, p *profile.Profile, runID string, timeout, poll time.Duration) error {
	if poll <= 0 {
		return fmt.Errorf("runs status: --poll must be > 0")
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		status, err := orchestrator.RunStatus(ctx, paths, p, runID)
		if err != nil {
			return exitError(3, err)
		}
		if isTerminalRunStatus(status) {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return exitErrorf(2, "runs status: timeout after %s waiting on %s", timeout, runID)
		}
		select {
		case <-ctx.Done():
			return exitError(4, ctx.Err())
		case <-ticker.C:
		}
	}
}
