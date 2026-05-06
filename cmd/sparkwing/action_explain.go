// `sparkwing pipeline explain <name>` forwards to the pipeline binary
// with --explain, which builds the Plan and emits its JSON snapshot
// without dispatching any jobs. The wrapper pretty-prints the plan
// (Plan -> Node -> Work -> Step) for humans by default; --json passes
// the binary's output through unchanged for agent consumption.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// planSnapshotDoc mirrors the shape pkg/orchestrator emits. Kept
// separate to avoid importing the orchestrator for just the schema.
// PR5 expanded the wire shape: each node now carries its
// inner Work (Steps + SpawnNode + SpawnNodeForEach), and Plan-layer
// modifiers move into a dedicated `modifiers` block so renderers can
// label each node with its dispatch envelope.
type planSnapshotDoc struct {
	Pipeline string `json:"pipeline"`
	RunID    string `json:"run_id"`
	// Venue mirrors orchestrator.planSnapshot.Venue: the author-
	// declared dispatch constraint, "" for pre-IMP-011 binaries.
	Venue string             `json:"venue,omitempty"`
	Nodes []planSnapshotNode `json:"nodes"`
}

type planSnapshotNode struct {
	ID          string                 `json:"id"`
	Deps        []string               `json:"deps,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	Groups      []string               `json:"groups,omitempty"`
	Dynamic     bool                   `json:"dynamic,omitempty"`
	Approval    *planSnapshotApprove   `json:"approval,omitempty"`
	OnFailureOf string                 `json:"on_failure_of,omitempty"`
	Modifiers   *planSnapshotModifiers `json:"modifiers,omitempty"`
	Work        *planSnapshotWork      `json:"work,omitempty"`
}

type planSnapshotApprove struct {
	Message   string `json:"message,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
	OnTimeout string `json:"on_timeout,omitempty"`
}

type planSnapshotModifiers struct {
	Retry           int      `json:"retry,omitempty"`
	RetryBackoffMS  int64    `json:"retry_backoff_ms,omitempty"`
	RetryAuto       bool     `json:"retry_auto,omitempty"`
	TimeoutMS       int64    `json:"timeout_ms,omitempty"`
	RunsOn          []string `json:"runs_on,omitempty"`
	CacheKey        string   `json:"cache_key,omitempty"`
	CacheMax        int      `json:"cache_max,omitempty"`
	CacheOnLimit    string   `json:"cache_on_limit,omitempty"`
	Inline          bool     `json:"inline,omitempty"`
	Optional        bool     `json:"optional,omitempty"`
	ContinueOnError bool     `json:"continue_on_error,omitempty"`
	OnFailure       string   `json:"on_failure,omitempty"`
	HasBeforeRun    bool     `json:"has_before_run,omitempty"`
	HasAfterRun     bool     `json:"has_after_run,omitempty"`
	HasSkipIf       bool     `json:"has_skip_if,omitempty"`
}

type planSnapshotWork struct {
	Steps      []planSnapshotStep      `json:"steps,omitempty"`
	Spawns     []planSnapshotSpawn     `json:"spawns,omitempty"`
	SpawnEach  []planSnapshotSpawnEach `json:"spawn_each,omitempty"`
	ResultStep string                  `json:"result_step,omitempty"`
}

type planSnapshotStep struct {
	ID        string   `json:"id"`
	Needs     []string `json:"needs,omitempty"`
	IsResult  bool     `json:"is_result,omitempty"`
	HasSkipIf bool     `json:"has_skip_if,omitempty"`
	// BlastRadius mirrors orchestrator.snapshotStep.BlastRadius:
	// the author-declared marker set, "" for pre-IMP-015 binaries.
	BlastRadius []string `json:"blast_radius,omitempty"`
}

type planSnapshotSpawn struct {
	ID         string            `json:"id"`
	Needs      []string          `json:"needs,omitempty"`
	TargetJob  string            `json:"target_job,omitempty"`
	TargetWork *planSnapshotWork `json:"target_work,omitempty"`
	HasSkipIf  bool              `json:"has_skip_if,omitempty"`
}

type planSnapshotSpawnEach struct {
	ID               string            `json:"id"`
	Needs            []string          `json:"needs,omitempty"`
	TargetJob        string            `json:"target_job,omitempty"`
	ItemTemplateWork *planSnapshotWork `json:"item_template_work,omitempty"`
	Note             string            `json:"note,omitempty"`
}

// pipelineExplainArgs holds the parsed wrapper-owned flags plus the
// trailing tokens that should be forwarded to the inner pipeline
// binary.
type pipelineExplainArgs struct {
	output      string
	asJSON      bool
	pipeline    string
	all         bool
	passthrough []string
}

// parsePipelineExplainArgs hand-parses the wrapper's own flags
// (--output / --json / --name / --all / --help) and treats every
// other token -- including everything after a literal "--"
// separator -- as passthrough for the inner pipeline binary.
//
// The "--" separator is consumed (not forwarded). Forwarding it to
// the pipeline binary would cause Go's flag package to stop flag
// parsing at that point, which is exactly what we need to
// avoid: `pipeline explain --name X -- --skip artifact` must reach
// the inner binary as `--explain --skip artifact`, not
// `--explain -- --skip artifact`.
func parsePipelineExplainArgs(args []string) (pipelineExplainArgs, bool, error) {
	var parsed pipelineExplainArgs
	for i := 0; i < len(args); i++ {
		tok := args[i]
		switch {
		case tok == "-h", tok == "--help":
			return parsed, true, nil
		case tok == "--":
			// End of wrapper flags; everything that follows is
			// forwarded raw to the inner pipeline binary. Drop
			// the "--" itself so the inner flag parser keeps
			// processing the trailing tokens as flags.
			parsed.passthrough = append(parsed.passthrough, args[i+1:]...)
			return parsed, false, nil
		case tok == "--json", tok == "--json=true":
			parsed.asJSON = true
		case tok == "--json=false":
			parsed.asJSON = false
		case tok == "--all", tok == "--all=true":
			parsed.all = true
		case tok == "--all=false":
			parsed.all = false
		case tok == "-o", tok == "--output":
			if i+1 >= len(args) {
				return parsed, false, errors.New("explain: --output expects a value")
			}
			parsed.output = args[i+1]
			i++
		case strings.HasPrefix(tok, "--output="):
			parsed.output = strings.TrimPrefix(tok, "--output=")
		case strings.HasPrefix(tok, "-o="):
			parsed.output = strings.TrimPrefix(tok, "-o=")
		case tok == "--name":
			if i+1 >= len(args) {
				return parsed, false, errors.New("explain: --name expects a value")
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
		PrintHelp(cmdPipelineExplain, os.Stdout)
		return nil
	}
	output := parsed.output
	asJSON := parsed.asJSON
	pipeline := parsed.pipeline
	all := parsed.all
	passthrough := parsed.passthrough
	if all {
		if pipeline != "" {
			return errors.New("explain: --all and --name are mutually exclusive")
		}
		if len(passthrough) > 0 {
			return fmt.Errorf("explain: --all does not accept pipeline-specific flags (got %v)", passthrough)
		}
		format, err := resolveOutputFormat(output, asJSON, cmdPipelineExplain.Path)
		if err != nil {
			return err
		}
		return runPipelineExplainAll(format)
	}
	if pipeline == "" {
		PrintHelp(cmdPipelineExplain, os.Stderr)
		return errors.New("explain: --name or --all is required")
	}
	format, err := resolveOutputFormat(output, asJSON, cmdPipelineExplain.Path)
	if err != nil {
		return err
	}
	jsonOut := format == "json"
	pipelineArgs := append([]string{pipeline, "--explain"}, passthrough...)
	// Reuse the compile+exec path by spawning 'wing' directly. `wing`
	// lives on PATH after `wing install`; fall back to `sparkwing
	// pipeline run <name> --explain` when it's not reachable so this
	// works from environments that only installed the server binary.
	binary := "wing"
	if _, err := exec.LookPath(binary); err != nil {
		binary = "sparkwing"
		pipelineArgs = append([]string{"pipelines", "run"}, pipelineArgs...)
	}
	var stdout bytes.Buffer
	cmd := exec.Command(binary, pipelineArgs...)
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("explain: %w", err)
	}
	if jsonOut {
		os.Stdout.Write(stdout.Bytes())
		return nil
	}
	var snap planSnapshotDoc
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &snap); err != nil {
		// Fallback: if the output isn't parseable JSON, dump it raw so
		// the operator sees what actually came back rather than a
		// cryptic decode error.
		os.Stdout.Write(stdout.Bytes())
		return nil
	}
	printPlanSnapshot(&snap)
	return nil
}

// allExplainResult is one row of the --all sweep. Status is one of
// "ok", "fail" (Plan-construction error -- gates the exit code), or
// "skipped" (pipeline requires Inputs that have no default; reported
// for visibility but does not fail the sweep -- Inputs validation is
// a separate concern)..
type allExplainResult struct {
	Pipeline string `json:"pipeline"`
	Status   string `json:"status"` // ok | fail | skipped
	Nodes    int    `json:"nodes,omitempty"`
	Error    string `json:"error,omitempty"`
}

// runPipelineExplainAll iterates every pipeline in the local
// .sparkwing/pipelines.yaml catalog, runs `pipeline explain` against
// each with zero arguments, and aggregates pass/fail. Non-zero exit on
// any failure makes this a CI gate: a Plan-time mismatch (e.g. a stale
// sparkwing.Output[T] call against a renamed output type) blocks merges.
func runPipelineExplainAll(format string) error {
	catalog, err := gatherPipelinesCatalog(true)
	if err != nil {
		return fmt.Errorf("explain --all: catalog: %w", err)
	}
	if len(catalog) == 0 {
		return errors.New("explain --all: no pipelines found in .sparkwing/pipelines.yaml")
	}
	binary := "wing"
	pipelinePrefix := []string{}
	if _, err := exec.LookPath(binary); err != nil {
		binary = "sparkwing"
		pipelinePrefix = []string{"pipelines", "run"}
	}
	results := make([]allExplainResult, 0, len(catalog))
	failed := 0
	for _, p := range catalog {
		row := allExplainResult{Pipeline: p.Name}
		pipelineArgs := append(append([]string(nil), pipelinePrefix...), p.Name, "--explain")
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(binary, pipelineArgs...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			row.Error = msg
			if isMissingInputsError(msg) {
				row.Status = "skipped"
			} else {
				row.Status = "fail"
				failed++
			}
		} else {
			var snap planSnapshotDoc
			if jerr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &snap); jerr == nil {
				row.Status = "ok"
				row.Nodes = len(snap.Nodes)
			} else {
				row.Status = "fail"
				row.Error = fmt.Sprintf("explain returned non-JSON output: %v", jerr)
				failed++
			}
		}
		results = append(results, row)
	}
	if format == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		printAllExplainTable(results, failed)
	}
	if failed > 0 {
		return fmt.Errorf("explain --all: %d of %d pipelines failed", failed, len(results))
	}
	return nil
}

// isMissingInputsError detects the "pipeline cannot construct a Plan
// without explicit args" failure mode so --all can report it without
// failing the gate. Inputs validation is a separate concern from
// Plan-construction validity.
func isMissingInputsError(msg string) bool {
	if strings.Contains(msg, "panic:") ||
		strings.Contains(msg, "runtime error") ||
		strings.Contains(msg, "[running]") {
		return false
	}
	return strings.Contains(msg, "build plan:") ||
		strings.Contains(msg, "inputs for pipeline")
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

// printPlanSnapshot renders the snapshot as a tree:
func printPlanSnapshot(snap *planSnapshotDoc) {
	if snap.Pipeline != "" {
		fmt.Printf("Plan: %s\n", snap.Pipeline)
	}
	// IMP-011: surface the dispatch constraint near the top so an
	// operator scanning the explain output sees the gate before
	// reading the DAG. Suppressed for the permissive default.
	if snap.Venue != "" && snap.Venue != "either" {
		fmt.Printf("Venue: %s\n", snap.Venue)
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

func printNode(n *planSnapshotNode, indent string) {
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

func nodeModifiersSuffix(n *planSnapshotNode) string {
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
			bits = append(bits, "RunsOn="+strings.Join(m.RunsOn, ","))
		}
		if m.CacheKey != "" {
			s := "Cache=" + m.CacheKey
			if m.CacheMax > 1 {
				s += fmt.Sprintf("(max=%d)", m.CacheMax)
			}
			if m.CacheOnLimit != "" && m.CacheOnLimit != "queue" {
				s += "(" + m.CacheOnLimit + ")"
			}
			bits = append(bits, s)
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

func printWork(w *planSnapshotWork, indent string) {
	fmt.Printf("%sWork\n", indent)
	for _, s := range w.Steps {
		marker := ""
		if s.IsResult {
			marker = " [result]"
		}
		if s.HasSkipIf {
			marker += " [skip_if]"
		}
		// IMP-015: surface blast-radius markers next to the step id
		// so a reader scanning explain output sees the contract
		// before reading the deps. Suppressed when no marker is
		// declared (the common case).
		if len(s.BlastRadius) > 0 {
			marker += " [" + strings.Join(s.BlastRadius, ",") + "]"
		}
		needs := ""
		if len(s.Needs) > 0 {
			needs = "  needs: " + strings.Join(s.Needs, ", ")
		}
		fmt.Printf("%s  Step %q%s%s\n", indent, s.ID, marker, needs)
	}
	for _, sp := range w.Spawns {
		needs := ""
		if len(sp.Needs) > 0 {
			needs = "  needs: " + strings.Join(sp.Needs, ", ")
		}
		skip := ""
		if sp.HasSkipIf {
			skip = " [skip_if]"
		}
		job := sp.TargetJob
		if job == "" {
			job = "<unknown>"
		}
		fmt.Printf("%s  SpawnNode %q (job=%s)%s%s\n", indent, sp.ID, job, skip, needs)
		if sp.TargetWork != nil {
			printWork(sp.TargetWork, indent+"    ")
		}
	}
	for _, e := range w.SpawnEach {
		needs := ""
		if len(e.Needs) > 0 {
			needs = "  needs: " + strings.Join(e.Needs, ", ")
		}
		job := e.TargetJob
		if job == "" {
			job = "<runtime>"
		}
		fmt.Printf("%s  SpawnNodeForEach %q (per item; job=%s)%s\n", indent, e.ID, job, needs)
		if e.Note != "" {
			fmt.Printf("%s    note: %s\n", indent, e.Note)
		}
		if e.ItemTemplateWork != nil {
			printWork(e.ItemTemplateWork, indent+"    ")
		}
	}
}

// printPlanEdges renders the Plan-level edges as a flat dependency
// list so a reader can quickly map the dispatch graph after seeing
// the per-node work breakdown.
func printPlanEdges(nodes []planSnapshotNode) {
	type edge struct{ From, To string }
	var edges []edge
	for _, n := range nodes {
		// Sort deps for stable output.
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
