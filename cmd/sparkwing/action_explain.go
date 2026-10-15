package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

type pipelineExplainArgs struct {
	output      string
	pipeline    string
	all         bool
	passthrough []string
}

func parsePipelineExplainArgs(args []string) (pipelineExplainArgs, bool, error) {
	var parsed pipelineExplainArgs
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-h", tok == "--help":
			return parsed, true, nil
		case tok == "--":
			parsed.passthrough = append(parsed.passthrough, args[i+1:]...)
			return parsed, false, nil
		case tok == "--all", tok == "--all=true":
			parsed.all = true
		case tok == "--all=false":
			parsed.all = false
		case tok == "-o", tok == "--output":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan --static: --output expects a value")
			}
			parsed.output = args[i+1]
			i++
		case strings.HasPrefix(tok, "--output="):
			parsed.output = strings.TrimPrefix(tok, "--output=")
		case strings.HasPrefix(tok, "-o="):
			parsed.output = strings.TrimPrefix(tok, "-o=")
		case tok == "--name":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan --static: --name expects a value")
			}
			parsed.pipeline = args[i+1]
			i++
		case strings.HasPrefix(tok, "--name="):
			parsed.pipeline = strings.TrimPrefix(tok, "--name=")
		default:
			parsed.passthrough = append(parsed.passthrough, tok)
		}
	}
	return parsed, false, nil
}

func runPipelineExplain(args []string) error {
	parsed, helpRequested, err := parsePipelineExplainArgs(args)
	if err != nil {
		return err
	}
	if helpRequested {
		PrintHelp(cmdPipelinePlan, os.Stdout)
		return nil
	}
	output := parsed.output
	pipeline := parsed.pipeline
	all := parsed.all
	passthrough := parsed.passthrough
	if all {
		if pipeline != "" {
			return errors.New("plan --static: --all and --name are mutually exclusive")
		}
		if len(passthrough) > 0 {
			return fmt.Errorf("plan --static: --all does not accept pipeline-specific flags (got %v)", passthrough)
		}
		format, err := resolveOutputFormat(output, cmdPipelinePlan.Path)
		if err != nil {
			return err
		}
		return runPipelineExplainAll(format)
	}
	if pipeline == "" {
		PrintHelp(cmdPipelinePlan, os.Stderr)
		return errors.New("plan --static: --name or --all is required")
	}
	format, err := resolveOutputFormat(output, cmdPipelinePlan.Path)
	if err != nil {
		return err
	}
	var snap nodeproto.PlanDoc
	err = withPipelineEntry(pipeline, func(ctx context.Context, entry pipelineentry.Command, dir string) error {
		planned, err := planOverProtocol(ctx, entry, dir, pipeline, passthrough, "explain")
		snap = planned.doc
		return err
	})
	if err != nil {
		return fmt.Errorf("plan --static: %w", err)
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(snap)
	}
	printPin(pipeline)
	fmt.Printf("Plan: %s\n", pipeline)
	printPlanSnapshot(&snap)

	fmt.Println()
	printTriggers(pipeline)
	return nil
}

type allExplainResult struct {
	Pipeline string `json:"pipeline"`
	Status   string `json:"status"`
	Nodes    int    `json:"nodes,omitempty"`
	Error    string `json:"error,omitempty"`
}

func runPipelineExplainAll(format string) error {
	catalog, err := gatherPipelinesCatalog(true)
	if err != nil {
		return fmt.Errorf("plan --static --all: catalog: %w", err)
	}
	if len(catalog) == 0 {
		return errors.New("plan --static --all: no pipelines found in .sparkwing/sparkwing.yaml")
	}
	results := make([]allExplainResult, 0, len(catalog))
	failed := 0
	for _, p := range catalog {
		row := allExplainResult{Pipeline: p.Name}
		err := withPipelineEntry(p.Name, func(ctx context.Context, entry pipelineentry.Command, dir string) error {
			planned, err := planOverProtocol(ctx, entry, dir, p.Name, nil, "explain")
			row.Nodes = len(planned.doc.Jobs)
			return err
		})
		switch {
		case err == nil:
			row.Status = "ok"
		case isInvalidArgs(err):
			row.Status, row.Error = "skipped", err.Error()
		default:
			row.Status, row.Error = "fail", err.Error()
			failed++
		}
		results = append(results, row)
	}
	if format == "json" {
		if err := ndjson.Write(os.Stdout, results); err != nil {
			return err
		}
	} else {
		printAllExplainTable(results, failed)
	}
	if failed > 0 {
		return fmt.Errorf("plan --static --all: %d of %d pipelines failed", failed, len(results))
	}
	return nil
}

func printAllExplainTable(results []allExplainResult, failed int) {
	nameWidth := 0
	for _, r := range results {
		if n := len(r.Pipeline); n > nameWidth {
			nameWidth = n
		}
	}
	skipped := 0
	for _, r := range results {
		switch r.Status {
		case "ok":
			fmt.Printf("  %-*s  ok (%d node%s)\n", nameWidth, r.Pipeline, r.Nodes, pluralS(r.Nodes))
		case "skipped":
			skipped++
			fmt.Printf("  %-*s  skipped (requires inputs: %s)\n", nameWidth, r.Pipeline, r.Error)
		default:
			fmt.Printf("  %-*s  FAIL: %s\n", nameWidth, r.Pipeline, r.Error)
		}
	}
	fmt.Println()
	total := len(results)
	ok := total - failed - skipped
	switch {
	case failed == 0 && skipped == 0:
		fmt.Printf("%d pipeline%s validated\n", total, pluralS(total))
	case failed == 0:
		fmt.Printf("%d of %d pipeline%s validated (%d skipped, requires inputs)\n",
			ok, total, pluralS(total), skipped)
	default:
		fmt.Printf("%d of %d pipeline%s validated; %d failed, %d skipped\n",
			ok, total, pluralS(total), failed, skipped)
	}
}

func printPlanSnapshot(snap *nodeproto.PlanDoc) {
	if len(snap.Jobs) == 0 {
		fmt.Println("(no nodes)")
		return
	}
	for _, n := range snap.Jobs {
		printNode(&n, "  ")
	}
	fmt.Println()
	printPlanEdges(snap.Jobs)
}

func printNode(n *nodeproto.JobSpec, indent string) {
	tag := "Node"
	if n.Approval != nil {
		tag = "Approval"
	}
	if n.OnFailureOf != "" {
		tag = "Recovery"
	}
	suffix := nodeModifiersSuffix(n)
	fmt.Printf("%s%s %q%s\n", indent, tag, n.ID, suffix)
	if n.Approval != nil {
		msg := n.Approval.Message
		if msg == "" {
			msg = "(default prompt)"
		}
		fmt.Printf("%s  prompt: %s\n", indent, msg)
		if n.Approval.TimeoutMS != nil && *n.Approval.TimeoutMS > 0 {
			fmt.Printf("%s  timeout: %s (on_timeout=%s)\n",
				indent, time.Duration(*n.Approval.TimeoutMS)*time.Millisecond, n.Approval.OnTimeout)
		}
	}
	if len(n.Steps) > 0 {
		printWork(n, indent+"  ")
	}
}

func nodeModifiersSuffix(n *nodeproto.JobSpec) string {
	var bits []string
	if len(n.Groups) > 0 {
		bits = append(bits, "groups="+strings.Join(n.Groups, ","))
	}
	if n.OnFailureOf != "" {
		bits = append(bits, "on_failure_of="+n.OnFailureOf)
	}
	if retry := n.Retry; retry != nil {
		label := fmt.Sprintf("Retry=%d", retry.Attempts)
		if retry.BackoffMS != nil {
			label += "@" + (time.Duration(*retry.BackoffMS) * time.Millisecond).String()
		}
		bits = append(bits, label)
	}
	if n.TimeoutMS != nil {
		bits = append(bits, "Timeout="+(time.Duration(*n.TimeoutMS)*time.Millisecond).String())
	}
	if len(n.Requires) > 0 {
		bits = append(bits, "Requires="+strings.Join(n.Requires, ","))
	}
	if n.Memo != nil {
		label := "Memo"
		if n.Memo.TTLMS != nil {
			label += "(ttl=" + (time.Duration(*n.Memo.TTLMS) * time.Millisecond).String() + ")"
		}
		bits = append(bits, label)
	}
	for _, membership := range n.Concurrency {
		parts := []string{"group=" + membership.Group, fmt.Sprintf("cap=%d", membership.Capacity), fmt.Sprintf("cost=%d", membership.Cost), "scope=" + membership.Scope, membership.OnLimit}
		if membership.QueueTimeoutMS != nil {
			parts = append(parts, "queue_timeout="+(time.Duration(*membership.QueueTimeoutMS)*time.Millisecond).String())
		}
		if membership.CancelTimeoutMS != nil {
			parts = append(parts, "cancel_timeout="+(time.Duration(*membership.CancelTimeoutMS)*time.Millisecond).String())
		}
		bits = append(bits, "Concurrency("+strings.Join(parts, " ")+")")
	}
	if n.Inline {
		bits = append(bits, "Inline")
	}
	if n.Optional {
		bits = append(bits, "Optional")
	}
	if n.ContinueOnError {
		bits = append(bits, "ContinueOnError")
	}
	if len(n.Closures.BeforeRun) > 0 {
		bits = append(bits, "BeforeRun")
	}
	if len(n.Closures.AfterRun) > 0 {
		bits = append(bits, "AfterRun")
	}
	if len(n.Closures.SkipIf) > 0 {
		bits = append(bits, "SkipIf")
	}
	if len(bits) == 0 {
		return ""
	}
	return " (" + strings.Join(bits, " ") + ")"
}

func printWork(w *nodeproto.JobSpec, indent string) {
	fmt.Printf("%sWork\n", indent)
	for _, s := range w.Steps {
		marker := ""
		if s.ID == w.ResultStep {
			marker = " [result]"
		}
		if len(s.Closures.SkipIf) > 0 {
			marker += " [skip_if]"
		}
		if len(s.Risks) > 0 {
			marker += " [" + strings.Join(s.Risks, ",") + "]"
		}
		if names := s.Groups; len(names) > 0 {
			marker += " (groups=" + strings.Join(names, ",") + ")"
		}
		needs := ""
		if len(s.Needs) > 0 {
			needs = "  needs: " + strings.Join(s.Needs, ", ")
		}
		fmt.Printf("%s  Step %q%s%s\n", indent, s.ID, marker, needs)
	}
}

func printPlanEdges(nodes []nodeproto.JobSpec) {
	type edge struct{ From, To string }
	var edges []edge
	for _, n := range nodes {
		deps := append([]string(nil), n.Needs...)
		sort.Strings(deps)
		for _, d := range deps {
			edges = append(edges, edge{From: d, To: n.ID})
		}
		if n.OnFailureOf != "" {
			edges = append(edges, edge{From: n.OnFailureOf + " (on_failure)", To: n.ID})
		}
	}
	if len(edges) == 0 {
		fmt.Println("(no plan-level dependencies)")
		return
	}
	fmt.Println("Plan edges:")
	for _, e := range edges {
		fmt.Printf("  %s -> %s\n", e.From, e.To)
	}
}
