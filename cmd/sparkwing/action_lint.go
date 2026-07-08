package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/pipelinelint"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

type pipelineLintArgs struct {
	output   string
	pipeline string
	dir      string
	chdir    string
	all      bool
	rules    bool
}

// parsePipelineLintArgs hand-parses lint's flags. lint takes no
// pipeline-binary passthrough (it never builds or runs a pipeline), so
// any unrecognized token is an error rather than forwarded.
func parsePipelineLintArgs(args []string) (pipelineLintArgs, bool, error) {
	parsed := pipelineLintArgs{output: "pretty"}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		valueFor := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("lint: %s expects a value", tok)
			}
			i++
			return args[i], nil
		}
		switch {
		case tok == "-h", tok == "--help":
			return parsed, true, nil
		case tok == "--all", tok == "--all=true":
			parsed.all = true
		case tok == "--all=false":
			parsed.all = false
		case tok == "--rules", tok == "--rules=true":
			parsed.rules = true
		case tok == "-o", tok == "--output":
			v, err := valueFor()
			if err != nil {
				return parsed, false, err
			}
			parsed.output = v
		case strings.HasPrefix(tok, "--output="):
			parsed.output = strings.TrimPrefix(tok, "--output=")
		case strings.HasPrefix(tok, "-o="):
			parsed.output = strings.TrimPrefix(tok, "-o=")
		case tok == "--name":
			v, err := valueFor()
			if err != nil {
				return parsed, false, err
			}
			parsed.pipeline = v
		case strings.HasPrefix(tok, "--name="):
			parsed.pipeline = strings.TrimPrefix(tok, "--name=")
		case tok == "--dir":
			v, err := valueFor()
			if err != nil {
				return parsed, false, err
			}
			parsed.dir = v
		case strings.HasPrefix(tok, "--dir="):
			parsed.dir = strings.TrimPrefix(tok, "--dir=")
		case tok == "-C", tok == "--sw-cd":
			v, err := valueFor()
			if err != nil {
				return parsed, false, err
			}
			parsed.chdir = v
		case strings.HasPrefix(tok, "--sw-cd="):
			parsed.chdir = strings.TrimPrefix(tok, "--sw-cd=")
		default:
			return parsed, false, fmt.Errorf("lint: unknown flag %q (see `sparkwing pipeline lint --help`)", tok)
		}
	}
	return parsed, false, nil
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
		return errors.New("lint: --all and --name are mutually exclusive")
	}
	if !parsed.all && parsed.pipeline == "" {
		PrintHelp(cmdPipelineLint, os.Stderr)
		return errors.New("lint: --name or --all is required")
	}
	if err := applyChdir(parsed.chdir); err != nil {
		return err
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

// resolveLintSourceDir picks the directory of pipeline source to scan.
// An explicit --dir wins; otherwise the convention is <.sparkwing>/jobs
// (where `pipeline new` scaffolds entrypoints), falling back to the
// .sparkwing directory itself when no jobs/ subdir exists.
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
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(findings)
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
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rules)
	}
	for i, r := range rules {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s\n  forbids: %s\n  why:     %s\n", r.Name, r.Forbids, r.Why)
	}
	return nil
}
