package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// runPipelineConfigInspect prints the declared secrets list (with
// per-field provenance and resolution status when a source is
// configured) for the given pipeline. Pure inspection: no Plan, no
// dispatch, no SecretResolver wiring beyond what the source binding
// would install.
//
// The verb retains the historical `<pipeline> config` name even
// though v0.6 removed the typed-Config surface; what's left is the
// secrets view. Future verb rename targeted for v0.7.
//
// Flags consumed from extra:
//
//	--output / -o   pretty | json (default pretty)
//	--json          alias for --output json
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
	pipelineYAML, _ := loadPipelineYAML(pipeline)
	sourceLabel := pipelineSourceLabel(pipelineYAML)

	secFields, err := sparkwing.InspectPipelineSecrets(context.Background(), reg, pipelineYAML, sourceLabel)
	if err != nil {
		return err
	}

	if format == "json" {
		return printConfigInspectJSON(pipeline, sourceLabel, secFields)
	}
	printConfigInspectPretty(os.Stdout, pipeline, sourceLabel, secFields)
	return nil
}

// pipelineSourceLabel returns a short label for the effective
// secrets backend for a pipeline run. Empty when no secrets backend
// applies. Pipeline-scoped overrides were dropped in v0.6, so this
// is always empty today; kept as a seam for the inspect verb to keep
// rendering a "source:" line when project defaults or profiles
// declare one.
func pipelineSourceLabel(_ *pipelines.Pipeline) string {
	return ""
}

func printConfigInspectPretty(w io.Writer, pipeline, source string, secFields []sparkwing.SecretField) {
	fmt.Fprintln(w, pipeline+" secrets")
	fmt.Fprintln(w)
	if len(secFields) == 0 {
		fmt.Fprintln(w, "  (none declared)")
		return
	}
	if source != "" {
		fmt.Fprintf(w, "source: %s\n\n", source)
	}
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
		fmt.Fprintf(w, "  %-*s  %s%s\n", nameWidth, s.Name, req, extra)
	}
}

func printConfigInspectJSON(pipeline, source string, secFields []sparkwing.SecretField) error {
	out := map[string]any{
		"pipeline": pipeline,
		"source":   source,
		"secrets":  secFields,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
