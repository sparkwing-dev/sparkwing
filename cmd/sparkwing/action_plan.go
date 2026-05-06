// `sparkwing pipeline plan <name>` forwards to the pipeline binary
// with --plan, which builds the Plan and emits a runtime-resolved
// preview JSON without dispatching any step body. Mirrors action_
// explain.go's wrapper shape: pipeline binary owns the JSON
// production, this wrapper owns flag parsing + pretty-printing.
// IMP-013.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// planPreviewDoc mirrors sparkwing.PlanPreview's wire shape. Kept
// separate to avoid pulling the SDK package into the wrapper just
// for the schema.
type planPreviewDoc struct {
	Pipeline string `json:"pipeline"`
	// Venue mirrors sparkwing.PlanPreview.Venue: the author-declared
	// dispatch constraint surfaced on the runtime-resolved view.
	// Empty for pre-IMP-011 pipeline binaries.
	Venue        string               `json:"venue,omitempty"`
	ResolvedArgs map[string]string    `json:"resolved_args,omitempty"`
	StartAt      string               `json:"start_at,omitempty"`
	StopAt       string               `json:"stop_at,omitempty"`
	LintWarnings []planPreviewLintDoc `json:"lint_warnings,omitempty"`
	Nodes        []planPreviewNodeDoc `json:"nodes"`
}

type planPreviewLintDoc struct {
	NodeID  string `json:"node_id,omitempty"`
	Message string `json:"message"`
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
	Steps     []planPreviewItemDoc `json:"steps,omitempty"`
	Spawns    []planPreviewItemDoc `json:"spawns,omitempty"`
	SpawnEach []planPreviewItemDoc `json:"spawn_each,omitempty"`
}

type planPreviewItemDoc struct {
	ID                string   `json:"id"`
	Needs             []string `json:"needs,omitempty"`
	Decision          string   `json:"decision"`
	SkipReason        string   `json:"skip_reason,omitempty"`
	SkipDetail        string   `json:"skip_detail,omitempty"`
	Cardinality       string   `json:"cardinality,omitempty"`
	CardinalitySource string   `json:"cardinality_source,omitempty"`
	// BlastRadius mirrors sparkwing.PreviewItem.BlastRadius:
	// canonical wire tokens for the author-declared marker set.
	// IMP-015.
	BlastRadius []string `json:"blast_radius,omitempty"`
}

// pipelinePlanArgs holds wrapper-owned flags + passthrough.
type pipelinePlanArgs struct {
	output      string
	asJSON      bool
	pipeline    string
	startAt     string
	stopAt      string
	passthrough []string
}

// parsePipelinePlanArgs is parsePipelineExplainArgs's twin: the
// same hand-parsed wrapper flags plus --start-at / --stop-at since
// those are part of the runtime-resolved view IMP-013 surfaces.
// Mirrors the explain parser's `--` separator handling exactly.
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
		case tok == "--json", tok == "--json=true":
			parsed.asJSON = true
		case tok == "--json=false":
			parsed.asJSON = false
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
		case tok == "--start-at":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan: --start-at expects a value")
			}
			parsed.startAt = args[i+1]
			i++
		case strings.HasPrefix(tok, "--start-at="):
			parsed.startAt = strings.TrimPrefix(tok, "--start-at=")
		case tok == "--stop-at":
			if i+1 >= len(args) {
				return parsed, false, errors.New("plan: --stop-at expects a value")
			}
			parsed.stopAt = args[i+1]
			i++
		case strings.HasPrefix(tok, "--stop-at="):
			parsed.stopAt = strings.TrimPrefix(tok, "--stop-at=")
		default:
			parsed.passthrough = append(parsed.passthrough, tok)
		}
	}
	return parsed, false, nil
}

func runPipelinePlan(args []string) error {
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
	// Hand-parsed: parsed.output is the empty string when -o/--output
	// was not provided, otherwise the user-supplied value. Use that to
	// drive the explicit-set bit the resolver wants (IMP-038).
	format, err := resolveOutputFormat(parsed.output, parsed.output != "", parsed.asJSON, cmdPipelinePlan.Path)
	if err != nil {
		return err
	}
	jsonOut := format == "json"

	pipelineArgs := append([]string{parsed.pipeline, "--plan"}, parsed.passthrough...)
	binary := "wing"
	if _, err := exec.LookPath(binary); err != nil {
		binary = "sparkwing"
		pipelineArgs = append([]string{"pipelines", "run"}, pipelineArgs...)
	}

	// Plumb --start-at / --stop-at via the same env-var contract
	// IMP-007 uses for the run path so the inner --plan handler
	// reads them identically to a real run.
	cmd := exec.Command(binary, pipelineArgs...)
	cmd.Env = append(os.Environ())
	if parsed.startAt != "" {
		cmd.Env = append(cmd.Env, "SPARKWING_START_AT="+parsed.startAt)
	}
	if parsed.stopAt != "" {
		cmd.Env = append(cmd.Env, "SPARKWING_STOP_AT="+parsed.stopAt)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if jsonOut {
		_, _ = os.Stdout.Write(stdout.Bytes())
		return nil
	}
	var doc planPreviewDoc
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &doc); err != nil {
		// Fall back to raw output so the operator sees what came
		// back rather than a cryptic decode error.
		_, _ = os.Stdout.Write(stdout.Bytes())
		return nil
	}
	printPlanPreview(&doc)
	return nil
}

// printPlanPreview renders a PlanPreview as a human-readable tree.
// Each node + work item is annotated with its would_run /
// would_skip decision and (for skips) the reason.
func printPlanPreview(doc *planPreviewDoc) {
	if doc.Pipeline != "" {
		fmt.Printf("Plan: %s\n", doc.Pipeline)
	}
	// IMP-011: surface the dispatch constraint right after the name
	// so a `pipeline plan` reader sees "venue: local-only" before
	// reading the DAG. Suppressed for the permissive default.
	if doc.Venue != "" && doc.Venue != "either" {
		fmt.Printf("Venue: %s\n", doc.Venue)
	}
	if doc.StartAt != "" || doc.StopAt != "" {
		fmt.Printf("Range: --start-at=%s --stop-at=%s\n",
			orDashStr(doc.StartAt), orDashStr(doc.StopAt))
	}
	if len(doc.ResolvedArgs) > 0 {
		fmt.Println("Args:")
		for k, v := range doc.ResolvedArgs {
			fmt.Printf("  %s=%s\n", k, v)
		}
	}
	if len(doc.LintWarnings) > 0 {
		fmt.Println("Lint warnings:")
		for _, lw := range doc.LintWarnings {
			if lw.NodeID != "" {
				fmt.Printf("  [%s] %s\n", lw.NodeID, lw.Message)
			} else {
				fmt.Printf("  %s\n", lw.Message)
			}
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
	// IMP-029: surface the recovery attachment so a `pipeline plan`
	// reader sees which parent's failure dispatches this node, not
	// just that the node is a recovery in the abstract.
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
	for _, sp := range w.Spawns {
		printPlanPreviewItem("SpawnNode", &sp, indent)
	}
	for _, sg := range w.SpawnEach {
		printPlanPreviewItem("SpawnNodeForEach", &sg, indent)
	}
}

func printPlanPreviewItem(kind string, it *planPreviewItemDoc, indent string) {
	decision := it.Decision
	if it.SkipReason != "" {
		decision += " (" + it.SkipReason + ")"
	}
	// IMP-015: append the blast-radius set inline so a `pipeline
	// plan` reader sees both the runtime decision and the contract
	// before drilling into needs / cardinality. Format mirrors
	// IMP-011's `Venue: <kind>` placement: tucked into the same
	// header line so the renderer stays compact for the common
	// no-marker case.
	if len(it.BlastRadius) > 0 {
		decision += " blast=" + strings.Join(it.BlastRadius, ",")
	}
	fmt.Printf("%s%s %q [%s]\n", indent, kind, it.ID, decision)
	if it.SkipDetail != "" {
		fmt.Printf("%s  reason: %s\n", indent, it.SkipDetail)
	}
	if it.Cardinality != "" {
		src := it.CardinalitySource
		if src == "" {
			src = "<unknown>"
		}
		fmt.Printf("%s  cardinality: %s (resolved at runtime from %s)\n", indent, it.Cardinality, src)
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
