package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/ndjson"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type Pipeline struct {
	Name       string                  `json:"name"`
	Short      string                  `json:"short,omitempty"`
	Help       string                  `json:"help,omitempty"`
	Triggers   []string                `json:"triggers,omitempty"`
	Entrypoint string                  `json:"entrypoint,omitempty"`
	Args       []sparkwing.DescribeArg `json:"args,omitempty"`
	Examples   []sparkwing.Example     `json:"examples,omitempty"`

	EnvVars []sparkwing.EnvVarDoc `json:"env_vars,omitempty"`

	Risks []string `json:"risks,omitempty"`

	RisksBySteps []sparkwing.DescribeStepRisks `json:"risks_by_step,omitempty"`
}

type PipelineIndex struct {
	Name       string   `json:"name"`
	Short      string   `json:"short,omitempty"`
	Entrypoint string   `json:"entrypoint,omitempty"`
	Triggers   []string `json:"triggers,omitempty"`
}

func (p Pipeline) index() PipelineIndex {
	short := p.Short
	if short == "" {
		short, _, _ = strings.Cut(p.Help, "\n")
	}
	return PipelineIndex{Name: p.Name, Short: short, Entrypoint: p.Entrypoint, Triggers: p.Triggers}
}

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
	case "lint":
		return runPipelineLint(args[1:])
	case "plan":
		return runPipelinePlan(args[1:])
	case "run":
		return dispatchRun(args[1:])
	case "trigger":
		return runPipelineTrigger(args[1:])
	case "publish":
		return runPipelinePublish(args[1:])
	case "hooks":
		return runHooks(args[1:])
	case "sparks":
		return runSparks(args[1:])
	default:
		PrintHelp(cmdPipeline, os.Stderr)
		return fmt.Errorf("pipeline: unknown subcommand %q", args[0])
	}
}

func chdirFlag(fs *flag.FlagSet) *string {
	return fs.StringP("sw-cd", "C", "", "operate as if started in this directory (re-anchors the .sparkwing search)")
}

func applyChdir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("--sw-cd %q: %w", dir, err)
	}
	return nil
}

func runPipelineList(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineList.Path, flag.ContinueOnError)
	output := fs.StringP("output", "o", "pretty", "output format: pretty | json | plain")
	includeHidden := fs.Bool("all", false, "include hidden entries (hidden: true in yaml / # hidden: true in scripts)")
	cd := chdirFlag(fs)
	if err := parseAndCheck(cmdPipelineList, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := applyChdir(*cd); err != nil {
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

		index := make([]PipelineIndex, 0, len(pipelines))
		for _, a := range pipelines {
			index = append(index, a.index())
		}
		return ndjson.Write(os.Stdout, index)
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
	cd := chdirFlag(fs)
	if err := parseAndCheck(cmdPipelineDiscover, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := applyChdir(*cd); err != nil {
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
		PipelineIndex
		Score int `json:"score"`
	}
	var results []scored
	for _, a := range pipelines {
		if s := scorePipeline(a, tokens); s > 0 {
			results = append(results, scored{PipelineIndex: a.index(), Score: s})
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

		return ndjson.Write(os.Stdout, results)
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
		fmt.Printf("  %-*s  %s\n", nameWidth, r.Name, r.Short)
	}
	return nil
}

func scorePipeline(a Pipeline, tokens []string) int {
	fields := []struct {
		weight int
		text   string
	}{
		{100, a.Name},
		{40, a.Short},
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
	cd := chdirFlag(fs)
	if err := parseAndCheck(cmdPipelineDescribe, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if err := applyChdir(*cd); err != nil {
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

func gatherPipelinesCatalog(includeHidden bool) ([]Pipeline, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	_, cfg, err := projectconfig.DiscoverPipelines(cwd)
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
				Entrypoint: p.Entrypoint,
				Triggers:   summarizeTriggerList(p.On),
			}
			if dp, ok := describeByName[p.Name]; ok {
				a.Short = dp.Short
				a.Help = dp.Help
				a.Args = dp.Args
				a.Examples = dp.Examples
				a.EnvVars = dp.EnvVars
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

func summarizeTriggerList(t pipelines.Triggers) []string {
	var out []string
	if t.Push != nil {
		if len(t.Push.Branches) > 0 {
			out = append(out, "push:"+strings.Join(t.Push.Branches, ","))
		} else {
			out = append(out, "push")
		}
	}
	if t.PullRequest != nil {
		if len(t.PullRequest.Branches) > 0 {
			out = append(out, "pull_request:"+strings.Join(t.PullRequest.Branches, ","))
		} else {
			out = append(out, "pull_request")
		}
	}
	if t.Webhook != nil {
		out = append(out, "webhook:"+t.Webhook.Path)
	}
	if t.Schedule != "" {
		out = append(out, "schedule:"+t.Schedule)
	}
	if t.PreHook != nil {
		out = append(out, "pre-commit")
	}
	if t.PostHook != nil {
		out = append(out, "pre-push")
	}
	if t.PostCommitHook != nil {
		out = append(out, "post-commit")
	}
	return out
}

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

func printPipelineDetail(a *Pipeline) {
	fmt.Printf("name:  %s\n", a.Name)
	if a.Entrypoint != "" {
		fmt.Printf("entrypoint: %s\n", a.Entrypoint)
	}
	if len(a.Risks) > 0 {
		fmt.Printf("risks: %s\n", strings.Join(a.Risks, ", "))
	}
	if len(a.Triggers) > 0 {
		fmt.Printf("triggers: %s\n", strings.Join(a.Triggers, ", "))
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
