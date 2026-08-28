package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

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
	secFields, err := sparkwing.InspectPipelineSecrets(context.Background(), reg, nil)
	if err != nil {
		return err
	}

	if format == "json" {
		return printConfigInspectJSON(pipeline, secFields)
	}
	printConfigInspectPretty(os.Stdout, pipeline, secFields)
	return nil
}

func printConfigInspectPretty(w io.Writer, pipeline string, secFields []sparkwing.SecretField) {
	fmt.Fprintln(w, pipeline+" secrets")
	fmt.Fprintln(w)
	if len(secFields) == 0 {
		fmt.Fprintln(w, "  (none declared)")
		return
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
		if s.GoField != "" {
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

func printConfigInspectJSON(pipeline string, secFields []sparkwing.SecretField) error {
	out := map[string]any{
		"pipeline": pipeline,
		"secrets":  secFields,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
