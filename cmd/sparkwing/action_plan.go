package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
)

type planPreviewDoc struct {
	Pipeline     string               `json:"pipeline"`
	ResolvedArgs map[string]string    `json:"resolved_args,omitempty"`
	StartAt      string               `json:"start_at,omitempty"`
	StopAt       string               `json:"stop_at,omitempty"`
	Nodes        []planPreviewNodeDoc `json:"nodes"`
}

type planPreviewNodeDoc struct {
	ID          string              `json:"id"`
	Deps        []string            `json:"deps,omitempty"`
	IsApproval  bool                `json:"is_approval,omitempty"`
	OnFailureOf string              `json:"on_failure_of,omitempty"`
	Decision    string              `json:"decision"`
	SkipReason  string              `json:"skip_reason,omitempty"`
	Work        *planPreviewWorkDoc `json:"work,omitempty"`
}

type planPreviewWorkDoc struct {
	Steps []planPreviewItemDoc `json:"steps,omitempty"`
}

type planPreviewItemDoc struct {
	ID         string   `json:"id"`
	Needs      []string `json:"needs,omitempty"`
	Decision   string   `json:"decision"`
	SkipReason string   `json:"skip_reason,omitempty"`
	SkipDetail string   `json:"skip_detail,omitempty"`

	Risks []string `json:"risks,omitempty"`
}

type pipelinePlanArgs struct {
	output      string
	pipeline    string
	startAt     string
	stopAt      string
	passthrough []string
}

func parsePipelinePlanArgs(args []string) (pipelinePlanArgs, bool, error) {
	var parsed pipelinePlanArgs
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-h", tok == "--help":
			return parsed, true, nil
		case tok == "--":
			parsed.passthrough = append(parsed.passthrough, args[i+1:]...)
			return parsed, false, nil
		case tok == "-o", tok == "--output":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan: --output expects a value")
			}
			parsed.output = args[i+1]
			i++
		case strings.HasPrefix(tok, "--output="):
			parsed.output = strings.TrimPrefix(tok, "--output=")
		case strings.HasPrefix(tok, "-o="):
			parsed.output = strings.TrimPrefix(tok, "-o=")
		case tok == "--name":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan: --name expects a value")
			}
			parsed.pipeline = args[i+1]
			i++
		case strings.HasPrefix(tok, "--name="):
			parsed.pipeline = strings.TrimPrefix(tok, "--name=")
		case tok == "--sw-start-at":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan: --start-at expects a value")
			}
			parsed.startAt = args[i+1]
			i++
		case strings.HasPrefix(tok, "--sw-start-at="):
			parsed.startAt = strings.TrimPrefix(tok, "--sw-start-at=")
		case tok == "--sw-stop-at":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan: --stop-at expects a value")
			}
			parsed.stopAt = args[i+1]
			i++
		case strings.HasPrefix(tok, "--sw-stop-at="):
			parsed.stopAt = strings.TrimPrefix(tok, "--sw-stop-at=")
		default:
			parsed.passthrough = append(parsed.passthrough, tok)
		}
	}
	return parsed, false, nil
}

func runPipelinePlan(args []string) error {
	if static, rest := takeStaticFlag(args); static {
		for _, tok := range rest {
			if strings.HasPrefix(tok, "--sw-start-at") || strings.HasPrefix(tok, "--sw-stop-at") || strings.HasPrefix(tok, "--start-at") || strings.HasPrefix(tok, "--stop-at") {
				return errors.New("plan --static: --start-at and --stop-at need the runtime plan; drop --static")
			}
		}
		return runPipelineExplain(rest)
	}
	parsed, helpRequested, err := parsePipelinePlanArgs(args)
	if err != nil {
		return err
	}
	if helpRequested {
		PrintHelp(cmdPipelinePlan, os.Stdout)
		return nil
	}
	if parsed.pipeline == "" {
		PrintHelp(cmdPipelinePlan, os.Stderr)
		return errors.New("plan: --name is required")
	}
	format, err := resolveOutputFormat(parsed.output, cmdPipelinePlan.Path)
	if err != nil {
		return err
	}
	var doc planPreviewDoc
	err = withPipelineBinary(parsed.pipeline, func(ctx context.Context, bin, dir string) error {
		planned, err := planOverProtocol(ctx, bin, dir, parsed.pipeline, parsed.passthrough, "plan")
		if err == nil {
			doc, err = previewPlan(planned, parsed.startAt, parsed.stopAt)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(doc)
	}
	printPin(parsed.pipeline)
	printPlanPreview(&doc)
	return nil
}

// safety: a pipeline with a declared source plans from that revision, as a run
// of it would execute.
func withPipelineBinary(pipeline string, fn func(ctx context.Context, bin, dir string) error) error {
	dir, err := findSparkwingDir()
	if err != nil {
		return err
	}
	if err := switchToolchain(dir); err != nil {
		return err
	}
	source, err := orchestrator.ResolvePipelineSource(context.Background(), filepath.Dir(dir), pipeline, "")
	if err != nil {
		return err
	}
	bindDir := dir
	if source != "" {
		_, selected, cleanup, err := setupRefWorktree(dir, string(source))
		if err != nil {
			return err
		}
		defer cleanup()
		dir = selected
	}
	run := newPipelineRun(dir, compileOptions{})
	defer run.stop()
	if err := run.materialize(os.Environ()); err != nil {
		return run.finish(err)
	}
	bin := run.uncachedBinary
	if bin == "" {
		bin = run.lease.Path()
	}
	return run.finish(fn(run.ctx, bin, filepath.Dir(bindDir)))
}

type planned struct {
	pipeline string
	args     map[string]string
	doc      nodeproto.PlanDoc
}

// safety: inspection never evaluates a closure. A skip_if predicate is customer code with side effects the
// protocol cannot seal, so plan reports it as decided at run time instead.
func planOverProtocol(ctx context.Context, bin, dir, pipeline string, args []string, runID string) (planned, error) {
	out := planned{pipeline: pipeline}
	err := repos.Converse(ctx, bin, dir, func(s *repos.Session) error {
		described, err := s.Describe()
		if err != nil {
			return err
		}
		schema, err := findPipelineSchema(described.Pipelines, pipeline)
		if err != nil {
			return err
		}
		if out.args, err = parsePipelineArgs(schema, args); err != nil {
			return &nodeproto.Error{Code: nodeproto.CodeInvalidArgs, Message: err.Error()}
		}
		req := nodeproto.Request{Op: nodeproto.OpPlan, Pipeline: pipeline, Args: out.args, Run: &nodeproto.RunContext{RunID: runID}}
		return s.Call(req, &out.doc)
	})
	return out, planError(pipeline, err)
}

type planFailure struct {
	msg   string
	cause *nodeproto.Error
}

func (e *planFailure) Error() string { return e.msg }
func (e *planFailure) Unwrap() error { return e.cause }

func planError(pipeline string, err error) error {
	var perr *nodeproto.Error
	switch {
	case err == nil:
		return nil
	case !errors.As(err, &perr):
		return fmt.Errorf("%s: %w", pipeline, err)
	case perr.Code == nodeproto.CodeInvalidArgs:
		return &planFailure{fmt.Sprintf("%s: invalid arguments: %s", pipeline, perr.Message), perr}
	case perr.Code == nodeproto.CodePlanFailed:
		return &planFailure{fmt.Sprintf("%s: the pipeline's Plan failed: %s", pipeline, perr.Message), perr}
	}
	return fmt.Errorf("%s: %w", pipeline, err)
}

func isInvalidArgs(err error) bool {
	var perr *nodeproto.Error
	return errors.As(err, &perr) && perr.Code == nodeproto.CodeInvalidArgs
}

func previewPlan(p planned, startAt, stopAt string) (planPreviewDoc, error) {
	doc := planPreviewDoc{Pipeline: p.pipeline, ResolvedArgs: p.args, StartAt: startAt, StopAt: stopAt}
	if err := checkStepRange(p.doc, startAt, stopAt); err != nil {
		return doc, err
	}
	for _, n := range p.doc.Nodes {
		pn := planPreviewNodeDoc{ID: n.ID, Deps: n.Deps, IsApproval: n.Approval != nil, OnFailureOf: n.OnFailureOf, Decision: "would_run"}
		if n.Work != nil && n.Approval == nil {
			pn.Work = &planPreviewWorkDoc{}
			ranged := rangeSkips(n.Work.Steps, startAt, stopAt)
			skipped := 0
			for _, s := range n.Work.Steps {
				it := planPreviewItemDoc{ID: s.ID, Needs: s.Needs, Decision: "would_run", Risks: s.Risks}
				if reason, ok := ranged[s.ID]; ok {
					it.Decision, it.SkipReason, it.SkipDetail = "would_skip", "range_skip", reason
				} else if s.Closures != nil && len(s.Closures.SkipIf) > 0 {
					it.Decision, it.SkipReason, it.SkipDetail = "may_skip", "user_skipif", "SkipIf is evaluated at run time"
				}
				if it.Decision == "would_skip" {
					skipped++
				}
				pn.Work.Steps = append(pn.Work.Steps, it)
			}
			if skipped > 0 && skipped == len(n.Work.Steps) {
				pn.Decision, pn.SkipReason = "would_skip", "all_steps_skipped"
			}
		}
		doc.Nodes = append(doc.Nodes, pn)
	}
	return doc, nil
}

func checkStepRange(doc nodeproto.PlanDoc, startAt, stopAt string) error {
	known := map[string]bool{}
	for _, n := range doc.Nodes {
		if n.Work != nil {
			for _, s := range n.Work.Steps {
				known[s.ID] = true
			}
		}
	}
	for _, bound := range [][2]string{{"--sw-start-at", startAt}, {"--sw-stop-at", stopAt}} {
		if bound[1] == "" || known[bound[1]] {
			continue
		}
		ids := slices.Sorted(maps.Keys(known))
		msg := fmt.Sprintf("%s %q references unknown step %q", bound[0], bound[1], bound[1])
		if suggestion := sparkwingruntime.SuggestClosest(bound[1], ids); suggestion != "" {
			msg += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		return fmt.Errorf("%s (available steps: %s)", msg, strings.Join(ids, ", "))
	}
	return nil
}

func rangeSkips(steps []nodeproto.PlanStep, startAt, stopAt string) map[string]string {
	parents := map[string][]string{}
	children := map[string][]string{}
	for _, s := range steps {
		parents[s.ID] = s.Needs
		for _, d := range s.Needs {
			children[d] = append(children[d], s.ID)
		}
	}
	_, hasStart := parents[startAt]
	_, hasStop := parents[stopAt]
	if !hasStart && !hasStop {
		return nil
	}
	reason := "upstream of --start-at=" + startAt
	switch {
	case startAt != "" && stopAt != "":
		reason = fmt.Sprintf("outside --start-at=%s --stop-at=%s range", startAt, stopAt)
	case stopAt != "":
		reason = "downstream of --stop-at=" + stopAt
	}
	skips := map[string]string{}
	for _, s := range steps {
		if (hasStart && !reaches(startAt, s.ID, children)) || (hasStop && !reaches(stopAt, s.ID, parents)) {
			skips[s.ID] = reason
		}
	}
	return skips
}

func reaches(from, to string, adj map[string][]string) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == to {
			return true
		}
		if !seen[n] {
			seen[n] = true
			stack = append(stack, adj[n]...)
		}
	}
	return false
}

func printPlanPreview(doc *planPreviewDoc) {
	if doc.Pipeline != "" {
		fmt.Printf("Plan: %s\n", doc.Pipeline)
	}
	if doc.StartAt != "" || doc.StopAt != "" {
		fmt.Printf("Range: --start-at=%s --stop-at=%s\n",
			orDashStr(doc.StartAt), orDashStr(doc.StopAt))
	}
	if len(doc.ResolvedArgs) > 0 {
		fmt.Println("Args:")
		for _, k := range slices.Sorted(maps.Keys(doc.ResolvedArgs)) {
			fmt.Printf("  %s=%s\n", k, doc.ResolvedArgs[k])
		}
	}
	if len(doc.Nodes) == 0 {
		fmt.Println("(no nodes)")
		return
	}
	for _, n := range doc.Nodes {
		printPlanPreviewNode(&n, "  ")
	}
}

func printPlanPreviewNode(n *planPreviewNodeDoc, indent string) {
	tag := "Node"
	if n.IsApproval {
		tag = "Approval"
	}
	if n.OnFailureOf != "" {
		tag = "Recovery"
	}
	decision := n.Decision
	if n.SkipReason != "" {
		decision += " (" + n.SkipReason + ")"
	}
	if n.OnFailureOf != "" {
		decision += " [OnFailure: " + n.OnFailureOf + "]"
	}
	fmt.Printf("%s%s %q [%s]\n", indent, tag, n.ID, decision)
	if n.Work != nil {
		printPlanPreviewWork(n.Work, indent+"  ")
	}
}

func printPlanPreviewWork(w *planPreviewWorkDoc, indent string) {
	for _, s := range w.Steps {
		printPlanPreviewItem("Step", &s, indent)
	}
}

func printPlanPreviewItem(kind string, it *planPreviewItemDoc, indent string) {
	decision := it.Decision
	if it.SkipReason != "" {
		decision += " (" + it.SkipReason + ")"
	}
	if len(it.Risks) > 0 {
		decision += " risks=" + strings.Join(it.Risks, ",")
	}
	fmt.Printf("%s%s %q [%s]\n", indent, kind, it.ID, decision)
	if it.SkipDetail != "" {
		fmt.Printf("%s  reason: %s\n", indent, it.SkipDetail)
	}
	if len(it.Needs) > 0 {
		fmt.Printf("%s  needs: %s\n", indent, strings.Join(it.Needs, ", "))
	}
}

func orDashStr(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func takeStaticFlag(args []string) (bool, []string) {
	rest := make([]string, 0, len(args))
	static := false
	for i, tok := range args {
		if tok == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if tok == "--static" || tok == "--static=true" {
			static = true
			continue
		}
		rest = append(rest, tok)
	}
	return static, rest
}
