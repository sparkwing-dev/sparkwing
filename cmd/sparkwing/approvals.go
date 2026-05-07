// Handlers for the approvals CLI verbs:
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/v2/controller/client"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// runApprove handles `sparkwing approve --run <id> --node <id>`.
func runApprove(ctx context.Context, paths orchestrator.Paths, args []string) error {
	return resolveApprovalVerb(ctx, paths, args, cmdApprove,
		store.ApprovalResolutionApproved, "approved")
}

// runDeny handles `sparkwing deny <run>/<node>`.
func runDeny(ctx context.Context, paths orchestrator.Paths, args []string) error {
	return resolveApprovalVerb(ctx, paths, args, cmdDeny,
		store.ApprovalResolutionDenied, "denied")
}

// resolveApprovalVerb is the shared body of approve/deny. Prints a
// one-line confirmation on success and exits non-zero on failure.
// Exit codes:
func resolveApprovalVerb(ctx context.Context, paths orchestrator.Paths, args []string, cmd Command, resolution, pastTense string) error {
	fs := flag.NewFlagSet(cmd.Path, flag.ContinueOnError)
	on := fs.String("on", "", "profile name (default: local)")
	comment := fs.String("comment", "", "optional operator note recorded on the approval")
	runIDFlag := fs.String("run", "", "run ID holding the approval gate")
	nodeIDFlag := fs.String("node", "", "node ID of the approval gate")
	if err := parseAndCheck(cmd, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected positional %q (use --run and --node)", cmd.Path, rest[0])
	}
	if *runIDFlag == "" || *nodeIDFlag == "" {
		return fmt.Errorf("%s: --run and --node are required", cmd.Path)
	}
	runID := *runIDFlag
	nodeID := *nodeIDFlag
	var err error

	approver := os.Getenv("USER")
	if approver == "" {
		approver = "unknown"
	}

	var got *store.Approval
	if *on == "" {
		got, err = resolveLocalApproval(ctx, paths, runID, nodeID, resolution, approver, *comment)
	} else {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, cmd.Path); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		got, err = c.ResolveApproval(ctx, runID, nodeID, resolution, approver, *comment)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s %s/%s by %s\n", pastTense, got.RunID, got.NodeID, got.Approver)
	return nil
}

// resolveLocalApproval opens the local SQLite store, writes the
// resolution, and returns the updated row. Keeps the store-close
// inside the function so callers don't need to manage a handle.
func resolveLocalApproval(ctx context.Context, paths orchestrator.Paths, runID, nodeID, resolution, approver, comment string) (*store.Approval, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.ResolveApproval(ctx, runID, nodeID, resolution, approver, comment)
}

// runApprovalsList handles `sparkwing approvals list`.
func runApprovalsList(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdApprovalsList.Path, flag.ContinueOnError)
	on := fs.String("on", "", "profile name (default: local)")
	runID := fs.String("run", "", "restrict to one run's approvals (pending + history)")
	outFmt := fs.StringP("output", "o", "", "output format: table|json|plain")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	if err := parseAndCheck(cmdApprovalsList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	resolvedFmt, rerr := resolveOutputFormat(*outFmt, fs.Changed("output"), *asJSON, cmdApprovalsList.Path)
	if rerr != nil {
		return rerr
	}
	emitJSON := resolvedFmt == "json"

	var rows []*store.Approval
	var err error
	if *on == "" {
		rows, err = listLocalApprovals(ctx, paths, *runID)
	} else {
		prof, perr := resolveProfile(*on)
		if perr != nil {
			return perr
		}
		if err := requireController(prof, cmdApprovalsList.Path); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		if *runID != "" {
			rows, err = c.ListApprovalsForRun(ctx, *runID)
		} else {
			rows, err = c.ListPendingApprovals(ctx)
		}
	}
	if err != nil {
		return err
	}
	if emitJSON {
		return json.NewEncoder(os.Stdout).Encode(rows)
	}
	return renderApprovalsTable(os.Stdout, rows)
}

// listLocalApprovals is the local-mode read for runApprovalsList.
// When runID is empty it returns unresolved approvals across every
// run; when set it returns the full history (pending + resolved) for
// that run.
func listLocalApprovals(ctx context.Context, paths orchestrator.Paths, runID string) ([]*store.Approval, error) {
	if err := paths.EnsureRoot(); err != nil {
		return nil, err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if runID != "" {
		return st.ListApprovalsForRun(ctx, runID)
	}
	return st.ListPendingApprovals(ctx)
}

func renderApprovalsTable(w *os.File, rows []*store.Approval) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tNODE\tREQUESTED\tSTATUS\tAPPROVER\tMESSAGE")
	for _, a := range rows {
		status := "pending"
		if a.ResolvedAt != nil {
			status = a.Resolution
		}
		age := time.Since(a.RequestedAt).Round(time.Second).String()
		msg := truncateOneLine(a.Message, 60)
		fmt.Fprintf(tw, "%s\t%s\t%s ago\t%s\t%s\t%s\n",
			a.RunID, a.NodeID, age, status, a.Approver, msg)
	}
	return tw.Flush()
}

// runApprovals routes the top-level `sparkwing approvals` verb to
// its subcommands. Parallels runJobs / runTokens / etc.
func runApprovals(args []string) error {
	if handleParentHelp(cmdApprovals, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdApprovals, os.Stderr)
		return errors.New("approvals: subcommand required")
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "list":
		return runApprovalsList(ctx, paths, args[1:])
	default:
		PrintHelp(cmdApprovals, os.Stderr)
		return fmt.Errorf("approvals: unknown command %q", args[0])
	}
}
