// `sparkwing pipeline` is the per-project pipeline surface. This file
// implements the dispatcher (runPipeline) and the read verbs (list,
// describe, discover, explain). The catalog merges
// .sparkwing/pipelines.yaml entries with the describe cache's
// typed metadata (Args, Examples) into one record shape.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Pipeline is the agent-facing record for one entry in this
// repo's pipelines.yaml. Pipelines with at least one trigger
// (push / webhook / schedule / hook) auto-run when the trigger
// fires; pipelines with an empty Triggers list are manual-only
// (`sparkwing run <name>`).
type Pipeline struct {
	Name       string                  `json:"name"`
	Short      string                  `json:"short,omitempty"`
	Help       string                  `json:"help,omitempty"`
	Hidden     bool                    `json:"hidden,omitempty"`
	Tags       []string                `json:"tags,omitempty"`
	Triggers   []string                `json:"triggers,omitempty"`
	Entrypoint string                  `json:"entrypoint,omitempty"`
	Args       []sparkwing.DescribeArg `json:"args,omitempty"`
	Examples   []sparkwing.Example     `json:"examples,omitempty"`
	// EnvVars are env vars the pipeline reads as inputs, surfaced via
	// the optional sparkwing.EnvVarDocer interface. Empty unless the
	// pipeline opts in.
	EnvVars []sparkwing.EnvVarDoc `json:"env_vars,omitempty"`
	// Risks is the sorted, deduplicated union of per-step risk
	// labels declared anywhere in the pipeline's plan. Mirrors
	// sparkwing.DescribePipeline.Risks. omitempty keeps the wire
	// format quiet for pipelines without risk labels.
	Risks []string `json:"risks,omitempty"`
	// RisksBySteps is the per-step breakdown of declared risk
	// labels. Mirrors sparkwing.DescribePipeline.RisksBySteps.
	RisksBySteps []sparkwing.DescribeStepRisks `json:"risks_by_step,omitempty"`
}

// runPipeline dispatches `sparkwing pipeline <verb> [...]`.
func runPipeline(args []string) error {
	if handleParentHelp(cmdPipeline, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdPipeline, os.Stderr)
		return errors.New("repo: subcommand required")
	}
	switch args[0] {
	case "list":
		return runPipelineList(args[1:])
	case "describe":
		return runPipelineDescribe(args[1:])
	case "discover":
		return runPipelineDiscover(args[1:])
	case "new":
		return runPipelineNew(args[1:])
	case "explain":
		return runPipelineExplain(args[1:])
	case "plan":
		return runPipelinePlan(args[1:])
	case "run":
		// Canonical run path. `sparkwing run <name>` is an alias for
		// this; both end up at the same dispatch entrypoint.
		return dispatchRun(args[1:])
	case "trigger":
		// Submit a trigger to a profile's controller for remote
		// execution; follows the run by default. The v0.5.0 successor
		// to `sparkwing run --sw-profile`.
		return runPipelineTrigger(args[1:])
	case "publish":
		// : compile + upload pipeline binary to the
		// configured ArtifactStore. Explicit operator action -- the
		// run path never auto-uploads.
		return runPipelinePublish(args[1:])
	case "hooks":
		// Git hooks fire pipelines on pre-commit / pre-push.
		return runHooks(args[1:])
	case "sparks":
		return runSparks(args[1:])
	default:
		PrintHelp(cmdPipeline, os.Stderr)
		return fmt.Errorf("pipeline: unknown subcommand %q", args[0])
	}
}

func runPipelineList(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineList.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format: pretty | json | plain")
	includeHidden := fs.Bool("all", false, "include hidden entries (hidden: true in yaml / # hidden: true in scripts)")
	if err := parseAndCheck(cmdPipelineList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*output, cmdPipelineList.Path)
	if err != nil {
		return err
	}
	pipelines, err := gatherPipelinesCatalog(*includeHidden)
	if err != nil {
		return err
	}
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pipelines)
	case "plain":
		for _, a := range pipelines {
			fmt.Println(a.Name)
		}
		return nil
	default:
		printPipelineTable(pipelines)
		return nil
	}
}

func runPipelineDiscover(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineDiscover.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format: pretty | json | plain")
	queryFlag := fs.String("query", "", "search query (one or more tokens; all must match some field)")
	if err := parseAndCheck(cmdPipelineDiscover, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdPipelineDiscover, os.Stderr)
		return fmt.Errorf("discover: unexpected positional %q (use --query)", fs.Arg(0))
	}
	if *queryFlag == "" {
		PrintHelp(cmdPipelineDiscover, os.Stderr)
		return errors.New("discover: --query is required")
	}
	format, err := resolveOutputFormat(*output, cmdPipelineDiscover.Path)
	if err != nil {
		return err
	}
	query := *queryFlag
	pipelines, err := gatherPipelinesCatalog(true)
	if err != nil {
		return err
	}
	tokens := strings.Fields(strings.ToLower(query))
	type scored struct {
		Pipeline
		Score int `json:"score"`
	}
	var results []scored
	for _, a := range pipelines {
		if s := scorePipeline(a, tokens); s > 0 {
			results = append(results, scored{Pipeline: a, Score: s})
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Name < results[j].Name
	})
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	case "plain":
		for _, r := range results {
			fmt.Println(r.Name)
		}
		return nil
	}
	if len(results) == 0 {
		fmt.Printf("no pipelines matched %q (try `sparkwing pipeline list` to see everything)\n", query)
		return nil
	}
	// Name column width capped at longest hit, 24 max -- one global
	// width keeps the score column aligned.
	const widthCap = 24
	nameWidth := 0
	for _, r := range results {
		if n := len(r.Name); n > nameWidth {
			nameWidth = n
		}
	}
	nameWidth = min(nameWidth, widthCap)
	fmt.Printf("query: %s (%d match%s)\n\n", query, len(results), plural(len(results)))
	for _, r := range results {
		short := r.Short
		if short == "" {
			short = r.Help
		}
		fmt.Printf("  %-*s  %s\n", nameWidth, r.Name, short)
	}
	return nil
}

// scorePipeline returns a positive relevance score if every query token
// hits some haystack field; 0 otherwise. Field weights favor name
// matches over description matches so `discover release` surfaces the
// `release` entry before an unrelated entry that merely mentions
// "release" in its long help text.
func scorePipeline(a Pipeline, tokens []string) int {
	fields := []struct {
		weight int
		text   string
	}{
		{100, a.Name},
		{40, a.Short},
		{25, strings.Join(a.Tags, " ")},
		{20, a.Help},
		{20, strings.Join(a.Triggers, " ")},
	}
	score := 0
	for _, tok := range tokens {
		best := 0
		for _, f := range fields {
			if strings.Contains(strings.ToLower(f.text), tok) && f.weight > best {
				best = f.weight
			}
		}
		if best == 0 {
			// Every token must match something; a token with no hit
			// means the overall match fails.
			return 0
		}
		score += best
	}
	return score
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}

func runPipelineDescribe(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineDescribe.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format: pretty | json | plain")
	pipelineName := fs.String("name", "", "pipeline name to describe")
	if err := parseAndCheck(cmdPipelineDescribe, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdPipelineDescribe, os.Stderr)
		return fmt.Errorf("describe: unexpected positional %q (use --name)", fs.Arg(0))
	}
	if *pipelineName == "" {
		PrintHelp(cmdPipelineDescribe, os.Stderr)
		return errors.New("describe: --name is required")
	}
	format, err := resolveOutputFormat(*output, cmdPipelineDescribe.Path)
	if err != nil {
		return err
	}
	name := *pipelineName
	// Describe always considers hidden entries -- the operator is
	// asking for a specific name, so opacity is a worse failure mode
	// than surface area.
	pipelines, err := gatherPipelinesCatalog(true)
	if err != nil {
		return err
	}
	var found *Pipeline
	for i := range pipelines {
		if pipelines[i].Name == name {
			found = &pipelines[i]
			break
		}
	}
	if found == nil {
		// Surface a "did you mean X?" suggestion when the typo is
		// close to a registered name. Source the candidate set from
		// the catalog we just gathered
		// (rather than sparkwing.Registered() -- this CLI verb runs in
		// the sparkwing process, not the inner pipeline binary, so the
		// in-process registry is empty here). Far typos fall through
		// to the existing "list --all" hint.
		candidates := make([]string, 0, len(pipelines))
		for _, p := range pipelines {
			candidates = append(candidates, p.Name)
		}
		suggestion := sparkwingruntime.SuggestClosest(name, candidates)
		if suggestion != "" {
			return fmt.Errorf("no pipeline named %q; did you mean %q? (run `sparkwing pipeline list --all` to see every entry)", name, suggestion)
		}
		return fmt.Errorf("no pipeline named %q (run `sparkwing pipeline list --all` to see every entry)", name)
	}
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(found)
	case "plain":
		fmt.Println(found.Name)
		return nil
	default:
		printPipelineDetail(found)
		return nil
	}
}

// gatherPipelinesCatalog merges the three registries (pipelines.yaml, describe
// cache, scripts frontmatter) into one sorted slice. Sort order is
// alphabetical by name regardless of kind, matching the intent of
// `sparkwing pipeline list` as a flat catalog; grouping/bucketing is
// a rendering concern handled by printPipelineTable.
func gatherPipelinesCatalog(includeHidden bool) ([]Pipeline, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	_, cfg, err := pipelines.Discover(cwd)
	if err != nil {
		return nil, err
	}
	describeByName := map[string]sparkwing.DescribePipeline{}
	if sparkwingDir, ok := walkUpForSparkwing(cwd); ok {
		if schema, serr := readDescribeCache(sparkwingDir); serr == nil {
			for _, dp := range schema {
				describeByName[dp.Name] = dp
			}
		}
	}
	var out []Pipeline
	seen := map[string]struct{}{}
	if cfg != nil {
		for _, p := range cfg.Pipelines {
			if p.Hidden && !includeHidden {
				continue
			}
			a := Pipeline{
				Name:       p.Name,
				Hidden:     p.Hidden,
				Tags:       p.Tags,
				Entrypoint: p.Entrypoint,
				Triggers:   summarizeTriggerList(p.On),
			}
			if dp, ok := describeByName[p.Name]; ok {
				a.Short = dp.Short
				a.Help = dp.Help
				a.Args = dp.Args
				a.Examples = dp.Examples
				a.EnvVars = dp.EnvVars
				// Surface risk labels in
				// `pipeline list / describe -o json`.
				a.Risks = dp.Risks
				a.RisksBySteps = dp.RisksBySteps
			}
			seen[p.Name] = struct{}{}
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// summarizeTriggerList turns the Triggers struct into one short
// string per declared trigger. Each string is self-contained (kind
// + args), so agents consuming JSON can parse by prefix if they
// want to filter.
func summarizeTriggerList(t pipelines.Triggers) []string {
	var out []string
	if t.Push != nil {
		if len(t.Push.Branches) > 0 {
			out = append(out, "push:"+strings.Join(t.Push.Branches, ","))
		} else {
			out = append(out, "push")
		}
	}
	if t.Webhook != nil {
		out = append(out, "webhook:"+t.Webhook.Path)
	}
	if t.Schedule != "" {
		out = append(out, "schedule:"+t.Schedule)
	}
	if t.Deploy != nil {
		out = append(out, "deploy")
	}
	if t.PreHook != nil {
		out = append(out, "pre-commit")
	}
	if t.PostHook != nil {
		out = append(out, "pre-push")
	}
	return out
}

// printPipelineTable renders the catalog as a flat, aligned table.
// Sorted by name; no grouping. Switching between shell completion and
// `sparkwing pipeline list` is intentionally one mental model now.
func printPipelineTable(pipelineList []Pipeline) {
	if len(pipelineList) == 0 {
		fmt.Println("(no pipelines)")
		return
	}
	const widthCap = 30
	nameWidth := 0
	for _, a := range pipelineList {
		if n := len(a.Name); n > nameWidth {
			nameWidth = n
		}
	}
	nameWidth = min(nameWidth, widthCap)
	for _, a := range pipelineList {
		short := a.Short
		if short == "" {
			short = a.Help
		}
		fmt.Printf("  %-*s  %s\n", nameWidth, a.Name, short)
	}
}

// printPipelineDetail renders a single Pipeline for human reading.
// JSON output is handled separately in the caller; this is the
// fallback when --json is absent.
func printPipelineDetail(a *Pipeline) {
	fmt.Printf("name:  %s\n", a.Name)
	if a.Entrypoint != "" {
		fmt.Printf("entrypoint: %s\n", a.Entrypoint)
	}
	if len(a.Tags) > 0 {
		fmt.Printf("tags:  %s\n", strings.Join(a.Tags, ", "))
	}
	if len(a.Risks) > 0 {
		fmt.Printf("risks: %s\n", strings.Join(a.Risks, ", "))
	}
	if len(a.Triggers) > 0 {
		fmt.Printf("triggers: %s\n", strings.Join(a.Triggers, ", "))
	}
	if a.Hidden {
		fmt.Println("hidden: true")
	}
	if a.Short != "" {
		fmt.Printf("\nshort: %s\n", a.Short)
	}
	if a.Help != "" && a.Help != a.Short {
		fmt.Printf("\n%s\n", a.Help)
	}
	if len(a.Args) > 0 {
		fmt.Println("\nargs:")
		for _, x := range a.Args {
			tag := "[optional]"
			if x.Required {
				tag = "[required]"
			}
			dflt := ""
			if x.Default != "" {
				dflt = " (default: " + x.Default + ")"
			}
			fmt.Printf("  --%-20s %s %s  %s%s\n",
				x.Name+" <"+x.Type+">", tag, x.Type, x.Desc, dflt)
		}
	}
	if len(a.EnvVars) > 0 {
		fmt.Println("\nenvironment variables:")
		for _, e := range a.EnvVars {
			line := "  " + e.Name
			if e.Description != "" {
				line += "  " + e.Description
			}
			if e.Default != "" {
				line += "  (default: " + e.Default + ")"
			}
			fmt.Println(line)
		}
	}
	if len(a.Examples) > 0 {
		fmt.Println("\nexamples:")
		for i, e := range a.Examples {
			if i > 0 {
				fmt.Println()
			}
			if e.Comment != "" {
				fmt.Printf("  # %s\n", e.Comment)
			}
			fmt.Printf("  %s\n", e.Command)
		}
	}
}
