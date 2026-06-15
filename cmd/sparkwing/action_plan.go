// `sparkwing pipeline plan <name>` forwards to the pipeline binary
// with --plan, which builds the Plan and emits a runtime-resolved
// preview JSON without dispatching any step body. Mirrors action_
// explain.go's wrapper shape: pipeline binary owns the JSON
// production, this wrapper owns flag parsing + pretty-printing.
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
	Pipeline     string               `json:"pipeline"`
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
	// Risks mirrors sparkwing.PreviewItem.Risks: the author-declared
	// risk-label set on this step.
	Risks []string `json:"risks,omitempty"`
}

// pipelinePlanArgs holds wrapper-owned flags + passthrough.
type pipelinePlanArgs struct {
	output      string
	pipeline    string
	startAt     string
	stopAt      string
	passthrough []string
}

// parsePipelinePlanArgs is parsePipelineExplainArgs's twin: the
// same hand-parsed wrapper flags plus --start-at / --stop-at since
// those are part of the runtime-resolved view this verb surfaces.
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
	jsonOut := format == "json"

	pipelineArgs := []string{"pipeline", "run", parsed.pipeline, "--plan"}
	pipelineArgs = append(pipelineArgs, parsed.passthrough...)
	binary, err := os.Executable()
	if err != nil {
		binary = "sparkwing"
	}

	cmd := exec.Command(binary, pipelineArgs...)
	cmd.Env = os.Environ()
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
		printPlanPreviewItem("JobSpawn", &sp, indent)
	}
	for _, sg := range w.SpawnEach {
		printPlanPreviewItem("JobSpawnEach", &sg, indent)
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
