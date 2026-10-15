package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/pkg/color"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinelint"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type pipelineLintArgs struct {
	output   string
	pipeline string
	dir      string
	all      bool
	rules    bool
}

func parsePipelineLintArgs(args []string) (pipelineLintArgs, bool, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "lint"}, args...))
	if err != nil {
		return pipelineLintArgs{}, false, err
	}
	return pipelineLintArgs{output: inv.Output(color.IsInteractiveStdout()), pipeline: inv.Value("pipeline"), dir: inv.Value("dir"), all: !inv.Has("pipeline"), rules: inv.Bool("rules")}, inv.Help(), nil
}

func runPipelineLint(args []string) error {
	parsed, helpRequested, err := parsePipelineLintArgs(args)
	if err != nil {
		return err
	}
	if helpRequested {
		PrintHelp(cmdPipelineLint, os.Stdout)
		return nil
	}
	format, err := resolveOutputFormat(parsed.output, cmdPipelineLint.Path)
	if err != nil {
		return err
	}
	if parsed.rules {
		return printLintRules(format)
	}
	if parsed.all && parsed.pipeline != "" {
		return errors.New("lint: --all and --pipeline are mutually exclusive")
	}
	if !parsed.all && parsed.pipeline == "" {
		parsed.all = true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	path, cfg, err := projectconfig.DiscoverPipelines(cwd)
	if err != nil {
		return err
	}
	sourceDir, err := resolveLintSourceDir(parsed.dir, path)
	if err != nil {
		return err
	}

	findings, err := pipelinelint.Analyze(sourceDir, cfg)
	if err != nil {
		return fmt.Errorf("lint: %w", err)
	}

	if parsed.pipeline != "" {
		entrypoint := cfg.EntrypointsByName()[parsed.pipeline]
		if entrypoint == "" {
			return fmt.Errorf("lint: no pipeline named %q in %s", parsed.pipeline, filepath.Base(path))
		}
		findings = filterFindings(findings, parsed.pipeline, entrypoint)
	}

	if err := renderLintFindings(findings, format); err != nil {
		return err
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
