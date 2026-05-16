package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/sources"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// runPipelineConfigInspect prints the layered Config struct for the
// given pipeline + --for selection, plus the declared Secrets list
// with per-field provenance. Pure inspection: no Plan(), no
// dispatch, no SecretResolver wiring beyond what the source binding
// would install.
//
// Flags consumed from extra:
//
//	--output / -o   pretty | json (default pretty)
//	--json          alias for --output json
//
// --for is honored via the SPARKWING_FOR env var the outer wing CLI
// already forwards; no separate parse here.
func runPipelineConfigInspect(pipeline string, extra []string) error {
	format := "pretty"
	for i := 0; i < len(extra); i++ {
		tok := extra[i]
		switch {
		case tok == "--json", tok == "--json=true":
			format = "json"
		case tok == "-o", tok == "--output":
			if i+1 < len(extra) {
				format = extra[i+1]
				i++
			}
		case strings.HasPrefix(tok, "-o="), strings.HasPrefix(tok, "--output="):
			format = tok[strings.IndexByte(tok, '=')+1:]
		}
	}
	if format != "pretty" && format != "json" {
		return fmt.Errorf("--output %q: must be pretty|json", format)
	}

	reg, ok := sparkwing.Lookup(pipeline)
	if !ok {
		return unknownPipelineErr(pipeline)
	}
	pipelineYAML, sparkwingDir := loadPipelineYAML(pipeline)
	target := os.Getenv("SPARKWING_FOR")

	if pipelineYAML != nil {
		if err := validateTargetSelection(Options{
			Pipeline:     pipeline,
			Target:       target,
			PipelineYAML: pipelineYAML,
		}); err != nil {
			return err
		}
	}

	cfgFields, err := sparkwing.InspectPipelineConfig(reg, pipelineYAML, target)
	if err != nil {
		return err
	}

	sourceName := pickSourceName(pipelineYAML, target, sparkwingDir)

	secFields, err := sparkwing.InspectPipelineSecrets(context.Background(), reg, pipelineYAML, sourceName)
	if err != nil {
		return err
	}

	if format == "json" {
		return printConfigInspectJSON(pipeline, target, sourceName, cfgFields, secFields)
	}
	printConfigInspectPretty(pipeline, target, sourceName, cfgFields, secFields)
	return nil
}

// pickSourceName returns the sources.yaml entry name that backs
// the pipeline run, taking the target's bound source first and
// falling back to the sources.yaml default. Empty when nothing
// applies.
func pickSourceName(p *pipelines.Pipeline, target, sparkwingDir string) string {
	if p != nil && target != "" {
		if t, ok := p.Targets[target]; ok && t.Source != "" {
			return t.Source
		}
	}
	return defaultSourceName(sparkwingDir)
}

func defaultSourceName(sparkwingDir string) string {
	user, err := sources.UserConfigPath()
	if err != nil {
		return ""
	}
	uf, _ := sources.Load(user)
	if sparkwingDir != "" {
		if rf, err := sources.Load(sources.RepoConfigPath(sparkwingDir)); err == nil && rf.Default != "" {
			return rf.Default
		}
	}
	return uf.Default
}

func printConfigInspectPretty(pipeline, target, source string, cfgFields []sparkwing.ConfigField, secFields []sparkwing.SecretField) {
	header := pipeline + " config"
	if target != "" {
		header += " (--for " + target + ")"
	}
	fmt.Println(header)
	fmt.Println()
	if len(cfgFields) == 0 {
		fmt.Println("  (pipeline declares no Config struct)")
	} else {
		nameWidth, valueWidth := 4, 5
		strVals := make([]string, len(cfgFields))
		for i, f := range cfgFields {
			if n := len(f.Name); n > nameWidth {
				nameWidth = n
			}
			strVals[i] = renderValue(f.Value)
			if n := len(strVals[i]); n > valueWidth {
				valueWidth = n
			}
		}
		for i, f := range cfgFields {
			req := ""
			if f.Required {
				req = " *required"
			}
			fmt.Printf("  %-*s = %-*s  [%s]%s\n",
				nameWidth, f.Name, valueWidth, strVals[i], f.Source, req)
		}
	}
	fmt.Println()
	if len(secFields) == 0 {
		fmt.Println("secrets: (none declared)")
		return
	}
	fmt.Printf("secrets (%d declared)", len(secFields))
	if source != "" {
		fmt.Printf("  source: %s", source)
	}
	fmt.Println(":")
	nameWidth := 4
	for _, s := range secFields {
		if n := len(s.Name); n > nameWidth {
			nameWidth = n
		}
	}
	for _, s := range secFields {
		req := "optional"
		if s.Required {
			req = "required"
		}
		extra := ""
		if s.GoField != "" && s.DeclaredIn == "pipelines.yaml secrets:" {
			extra = "  (also in Secrets struct as " + s.GoField + ")"
		} else if s.GoField != "" {
			extra = "  (Secrets struct: " + s.GoField + ")"
		} else if s.DeclaredIn != "" {
			extra = "  [" + s.DeclaredIn + "]"
		}
		if s.Note != "" {
			extra += "  -- " + s.Note
		}
		fmt.Printf("  %-*s  %s%s\n", nameWidth, s.Name, req, extra)
	}
}

func printConfigInspectJSON(pipeline, target, source string, cfgFields []sparkwing.ConfigField, secFields []sparkwing.SecretField) error {
	out := map[string]any{
		"pipeline": pipeline,
		"target":   target,
		"source":   source,
		"config":   cfgFields,
		"secrets":  secFields,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func renderValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return "\"\""
		}
		return t
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

// sortConfigFieldsByName is used by tests for deterministic output;
// the printer keeps declaration order.
func sortConfigFieldsByName(fs []sparkwing.ConfigField) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
}
