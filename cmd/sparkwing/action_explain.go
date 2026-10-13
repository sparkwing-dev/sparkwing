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
	err = withPipelineBinary(pipeline, func(ctx context.Context, bin, dir string) error {
		planned, err := planOverProtocol(ctx, bin, dir, pipeline, passthrough, "explain", false)
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
		err := withPipelineBinary(p.Name, func(ctx context.Context, bin, dir string) error {
			planned, err := planOverProtocol(ctx, bin, dir, p.Name, nil, "explain", false)
			row.Nodes = len(planned.doc.Nodes)
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
	if snap.Pipeline != "" {
		fmt.Printf("Plan: %s\n", snap.Pipeline)
	}
	if len(snap.Nodes) == 0 {
		fmt.Println("(no nodes)")
		return
	}
	for _, n := range snap.Nodes {
		printNode(&n, "  ")
	}
	fmt.Println()
	printPlanEdges(snap.Nodes)
}

func printNode(n *nodeproto.PlanNode, indent string) {
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
		if n.Approval.TimeoutMS > 0 {
			fmt.Printf("%s  timeout: %s (on_timeout=%s)\n",
				indent, time.Duration(n.Approval.TimeoutMS)*time.Millisecond, n.Approval.OnTimeout)
		}
	}
	if n.Work != nil {
		printWork(n.Work, indent+"  ")
	}
}

func nodeModifiersSuffix(n *nodeproto.PlanNode) string {
	var bits []string
	if n.Dynamic {
		bits = append(bits, "dynamic")
	}
	if len(n.Groups) > 0 {
		bits = append(bits, "groups="+strings.Join(n.Groups, ","))
	}
	if n.OnFailureOf != "" {
		bits = append(bits, "on_failure_of="+n.OnFailureOf)
	}
	if m := n.Modifiers; m != nil {
		if m.Retry > 0 {
			label := "Retry"
			if m.RetryAuto {
				label = "Retry(auto)"
			}
			s := fmt.Sprintf("%s=%d", label, m.Retry)
			if m.RetryBackoffMS > 0 {
				s += "@" + (time.Duration(m.RetryBackoffMS) * time.Millisecond).String()
			}
			bits = append(bits, s)
		}
		if m.TimeoutMS > 0 {
			bits = append(bits, "Timeout="+(time.Duration(m.TimeoutMS)*time.Millisecond).String())
		}
		if len(m.RunsOn) > 0 {
			bits = append(bits, "Requires="+strings.Join(m.RunsOn, ","))
		}
		if m.Cache {
			s := "Cache"
			if m.CacheTTLMS > 0 {
				s += "(ttl=" + (time.Duration(m.CacheTTLMS) * time.Millisecond).String() + ")"
			}
			bits = append(bits, s)
		}
		if m.ConcGroup != "" {
			parts := []string{"group=" + m.ConcGroup}
			if m.ConcCapacity > 0 {
				parts = append(parts, fmt.Sprintf("cap=%d", m.ConcCapacity))
			}
			if m.ConcCost > 1 {
				parts = append(parts, fmt.Sprintf("cost=%d", m.ConcCost))
			}
			if m.ConcScope != "" && m.ConcScope != "global" {
				parts = append(parts, "scope="+m.ConcScope)
			}
			if m.ConcOnLimit != "" && m.ConcOnLimit != "queue" {
				parts = append(parts, m.ConcOnLimit)
			}
			if m.ConcQueueTimeoutMS > 0 {
				parts = append(parts, "queue_timeout="+(time.Duration(m.ConcQueueTimeoutMS)*time.Millisecond).String())
			}
			if m.ConcCancelTimeoutMS > 0 {
				parts = append(parts, "cancel_timeout="+(time.Duration(m.ConcCancelTimeoutMS)*time.Millisecond).String())
			}
			bits = append(bits, "Concurrency("+strings.Join(parts, " ")+")")
		}
		if m.Inline {
			bits = append(bits, "Inline")
		}
		if m.Optional {
			bits = append(bits, "Optional")
		}
		if m.ContinueOnError {
			bits = append(bits, "ContinueOnError")
		}
		if m.OnFailure != "" {
			bits = append(bits, "OnFailure="+m.OnFailure)
		}
		if m.HasBeforeRun {
			bits = append(bits, "BeforeRun")
		}
		if m.HasAfterRun {
			bits = append(bits, "AfterRun")
		}
		if m.HasSkipIf {
			bits = append(bits, "SkipIf")
		}
	}
	if len(bits) == 0 {
		return ""
	}
	return " (" + strings.Join(bits, " ") + ")"
}

func printWork(w *nodeproto.PlanWork, indent string) {
	fmt.Printf("%sWork\n", indent)
	groupByStep := map[string][]string{}
	for _, g := range w.StepGroups {
		if g.Name == "" {
			continue
		}
		for _, m := range g.Members {
			groupByStep[m] = append(groupByStep[m], g.Name)
		}
	}
	for _, s := range w.Steps {
		marker := ""
		if s.IsResult {
			marker = " [result]"
		}
		if s.HasSkipIf {
			marker += " [skip_if]"
		}
		if len(s.Risks) > 0 {
			marker += " [" + strings.Join(s.Risks, ",") + "]"
		}
		if names := groupByStep[s.ID]; len(names) > 0 {
			marker += " (groups=" + strings.Join(names, ",") + ")"
		}
		needs := ""
		if len(s.Needs) > 0 {
			needs = "  needs: " + strings.Join(s.Needs, ", ")
		}
		fmt.Printf("%s  Step %q%s%s\n", indent, s.ID, marker, needs)
	}
}

func printPlanEdges(nodes []nodeproto.PlanNode) {
	type edge struct{ From, To string }
	var edges []edge
	for _, n := range nodes {
		deps := append([]string(nil), n.Deps...)
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
