// `sparkwing pipeline templates` lists the sparks-core template registry
// -- the curated, parameterized pipeline starters that `pipeline new
// --template <name>` renders. Distinct from the two built-in stubs
// (minimal, build-test-deploy): those ship in this binary; these come
// from the sparks-core/templates module.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/color"

	templates "github.com/sparkwing-dev/sparks-core/templates"
)

func runPipelineTemplates(args []string) error {
	fs := flag.NewFlagSet(cmdPipelineTemplates.Path, flag.ContinueOnError)
	var output string
	fs.StringVarP(&output, "output", "o", "pretty", "pretty | json")
	_ = chdirFlag(fs) // accepted for consistency; the registry is embedded, no cwd needed
	if err := parseAndCheck(cmdPipelineTemplates, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("templates: unexpected positional %q", fs.Arg(0))
	}

	list, err := templates.List()
	if err != nil {
		return fmt.Errorf("load templates: %w", err)
	}

	switch strings.ToLower(output) {
	case "json":
		manifests := make([]templates.Manifest, 0, len(list))
		for _, t := range list {
			manifests = append(manifests, t.Manifest)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(manifests)
	case "pretty", "":
		for _, t := range list {
			m := t.Manifest
			fmt.Println(color.Bold(m.Name))
			signal := strings.TrimSpace(m.WhenToUse)
			if signal == "" {
				signal = strings.TrimSpace(m.Description)
			}
			for _, line := range strings.Split(signal, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					fmt.Printf("  %s\n", color.Dim(line))
				}
			}
			var req, opt []string
			for _, p := range m.Parameters {
				if p.Required {
					req = append(req, p.Name)
				} else {
					opt = append(opt, p.Name)
				}
			}
			if len(req) > 0 {
				fmt.Printf("  %s %s\n", color.Bold("required:"), strings.Join(req, ", "))
			}
			if len(opt) > 0 {
				fmt.Printf("  %s %s\n", color.Dim("optional:"), color.Dim(strings.Join(opt, ", ")))
			}
			if pre := strings.TrimSpace(m.Prerequisite); pre != "" {
				fmt.Printf("  %s %s\n", color.Bold("prerequisite:"), pre)
			}
			fmt.Println()
		}
		fmt.Printf("%s sparkwing pipeline new --name <name> --template <template> --param k=v ...\n",
			color.Dim("scaffold:"))
		return nil
	default:
		return fmt.Errorf("unknown output format %q (valid: pretty, json)", output)
	}
}
