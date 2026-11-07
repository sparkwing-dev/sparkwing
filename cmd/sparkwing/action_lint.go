package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/pkg/color"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinelint"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type pipelineLintArgs struct {
	output   string
	pipeline string
	dir      string
	all      bool
	rules    bool
}

func pipelineLintOptions(inv clisurface.Invocation) pipelineLintArgs {
	return pipelineLintArgs{output: inv.Output(color.IsInteractiveStdout()), pipeline: inv.Value("pipeline"), dir: inv.Value("dir"), all: !inv.Has("pipeline"), rules: inv.Bool("rules")}
}

func runPipelineLint(inv clisurface.Invocation) error {
	parsed := pipelineLintOptions(inv)
	format, err := resolveOutputFormat(parsed.output, cmdPipelineLint.Path)
	if err != nil {
		return err
	}
	if parsed.rules {
		return printLintRules(format)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path, cfg, err := projectconfig.Discover(cwd)
	if err != nil {
		return err
	}
	sourceDir, err := resolveLintSourceDir(parsed.dir, path)
	if err != nil {
		return err
	}

	findings, err := pipelinelint.Analyze(sourceDir, cfg.Registry())
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}

	if parsed.pipeline != "" {
		entrypoint := cfg.Registry().EntrypointsByName()[parsed.pipeline]
		if entrypoint == "" {
			return fmt.Errorf("lint: no pipeline named %q in %s", parsed.pipeline, filepath.Base(path))
		}
		findings = filterFindings(findings, parsed.pipeline, entrypoint)
	}

	if err := renderLintFindings(findings, format); err != nil {
		return err
	}
	if format != "json" {
		if err := printPlacementPreview(cfg, parsed.pipeline); err != nil {
			return err
		}
	}
	if len(findings) > 0 {
		return fmt.Errorf("lint: %d violation%s", len(findings), pluralS(len(findings)))
	}
	return nil
}

func resolveLintSourceDir(dir, configPath string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	sparkwingDir := filepath.Dir(configPath)
	jobs := filepath.Join(sparkwingDir, "jobs")
	if info, err := os.Stat(jobs); err == nil && info.IsDir() {
		return jobs, nil
	}
	return sparkwingDir, nil
}

func filterFindings(findings []pipelinelint.Finding, name, entrypoint string) []pipelinelint.Finding {
	var out []pipelinelint.Finding
	for _, f := range findings {
		if f.Pipeline == name || f.Pipeline == entrypoint {
			out = append(out, f)
		}
	}
	return out
}

func renderLintFindings(findings []pipelinelint.Finding, format string) error {
	switch format {
	case "json":

		return ndjson.Write(os.Stdout, findings)
	case "plain":
		for _, f := range findings {
			if f.File != "" {
				fmt.Printf("%s:%d:%d: %s: %s\n", f.File, f.Line, f.Col, f.Rule, f.Message)
			} else {
				fmt.Printf("%s: [%s] %s\n", f.Pipeline, f.Rule, f.Message)
			}
		}
		return nil
	default:
		printLintTable(findings)
		return nil
	}
}

func printLintTable(findings []pipelinelint.Finding) {
	if len(findings) == 0 {
		fmt.Println("no violations")
		return
	}
	for _, f := range findings {
		loc := f.Pipeline
		if f.File != "" {
			loc = fmt.Sprintf("%s:%d:%d", filepath.Base(f.File), f.Line, f.Col)
		}
		fmt.Printf("  %s  [%s]\n      %s\n", loc, f.Rule, f.Message)
	}
	fmt.Println()
	fmt.Printf("%d violation%s\n", len(findings), pluralS(len(findings)))
}

func printLintRules(format string) error {
	rules := pipelinelint.Rules()
	if format == "json" {
		return ndjson.Write(os.Stdout, rules)
	}
	for i, r := range rules {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s\n  forbids: %s\n  why:     %s\n", r.Name, r.Forbids, r.Why)
	}
	return nil
}

// safety: the preview reads the local file, but only the default branch's copy applies to proven runs.
func printPlacementPreview(cfg *projectconfig.Config, only string) error {
	if cfg == nil {
		return nil
	}
	header := false
	for _, p := range cfg.Pipelines {
		if only != "" && p.Name != only {
			continue
		}
		policy, err := cfg.RepositoryPlacement(p.Name, "")
		if err != nil {
			return err
		}
		if policy == nil {
			continue
		}
		if !header {
			fmt.Println("\nplacement for repository runs (the default branch's copy applies):")
			header = true
		}
		fmt.Printf("  %s\n", p.Name)
		fmt.Printf("    %-12s %s\n", "other jobs", placementLine(policy.Prefer))
		for _, job := range policy.Jobs {
			fmt.Printf("    %-12s %s\n", job.Job, placementLine(job.Prefer))
		}
	}
	return nil
}

func placementLine(tiers []runspec.RepositoryTier) string {
	if len(tiers) == 0 {
		return "existing routing (no repository list)"
	}
	parts := make([]string, len(tiers))
	for i, tier := range tiers {
		parts[i] = tier.String()
	}
	return strings.Join(parts, " -> ")
}
