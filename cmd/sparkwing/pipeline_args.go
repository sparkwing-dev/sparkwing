package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/argvalue"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func pipelineIdentityAt(dir, name string) (clisurface.PipelineIdentity, error) {
	cfg, err := projectconfig.Load(filepath.Join(dir, projectconfig.Filename))
	if err != nil {
		return clisurface.PipelineIdentity{}, err
	}
	return clisurface.ResolvePipeline(cfg, name)
}

func boundPipelineSchema(schemas []nodeproto.PipelineDescription, identity clisurface.PipelineIdentity) (nodeproto.PipelineDescription, error) {
	schema, err := findPipelineSchema(schemas, identity.Entrypoint)
	if err != nil {
		return schema, fmt.Errorf("pipeline %q: %w", identity.Name, err)
	}
	schema.Name = identity.Name
	return schema, nil
}

func findPipelineSchema(schemas []nodeproto.PipelineDescription, pipelineName string) (nodeproto.PipelineDescription, error) {
	if schema, found := (nodeproto.DescribeDoc{Pipelines: schemas}).Pipeline(pipelineName); found {
		if schema.Error != nil {
			return nodeproto.PipelineDescription{}, schema.Error
		}
		return schema, nil
	}
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	msg := fmt.Sprintf("unknown pipeline %q", pipelineName)
	if suggestion := sparkwingruntime.SuggestClosest(pipelineName, names); suggestion != "" {
		msg += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return nodeproto.PipelineDescription{}, fmt.Errorf("%s", msg)
}

func parsePipelineArgs(schema nodeproto.PipelineDescription, args []string) (map[string]string, error) {
	byName := map[string]nodeproto.ArgumentDescription{}
	byShort := map[string]nodeproto.ArgumentDescription{}
	for _, a := range schema.Args {
		byName[a.Name] = a
		if a.Short != "" {
			byShort[a.Short] = a
		}
	}
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			return nil, fmt.Errorf("unexpected positional argument %q", tok)
		}
		dashes := "--"
		if !strings.HasPrefix(tok, dashes) {
			dashes = "-"
		}
		name := strings.TrimPrefix(tok, dashes)
		value, hasValue := "", false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value, hasValue = name[:eq], name[eq+1:], true
		}
		arg, found := byName[name]
		if dashes == "-" {
			arg, found = byShort[name]
		}
		if !found && !schema.Extra {
			return nil, fmt.Errorf("unknown flag %s%s (run `sparkwing run %s --help` for valid flags)", dashes, name, schema.Name)
		}
		if found {
			name = arg.Name
			if arg.Type == "bool" && !hasValue {
				value, hasValue = "true", true
			}
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("flag --%s expects a value", name)
			}
			i++
			value = args[i]
		}
		if found && len(arg.Enum) > 0 {
			values := []string{value}
			if arg.Type == "[]string" {
				values = argvalue.List(value)
			}
			for _, item := range values {
				if slices.Contains(arg.Enum, item) {
					continue
				}
				if arg.Secret {
					return nil, fmt.Errorf("flag --%s: invalid secret value", name)
				}
				return nil, fmt.Errorf("flag --%s: %q is not one of %s", name, item, strings.Join(arg.Enum, ", "))
			}
		}
		out[name] = value
	}
	return out, nil
}

func printPipelineHelp(w io.Writer, schema nodeproto.PipelineDescription) {
	if schema.Help != "" {
		fmt.Fprintf(w, "DESCRIPTION\n  %s\n\n", schema.Help)
	}
	fmt.Fprintf(w, "USAGE\n  sparkwing run %s", schema.Name)
	for _, a := range schema.Args {
		if a.Required {
			fmt.Fprintf(w, " --%s <%s>", a.Name, a.Type)
		}
	}
	if len(schema.Args) > 0 {
		fmt.Fprint(w, " [flags]")
	}
	fmt.Fprint(w, "\n\n")
	if len(schema.Args) == 0 {
		fmt.Fprint(w, "No pipeline-specific flags.\n\n")
	} else {
		fmt.Fprintln(w, "PIPELINE FLAGS")
		for _, a := range schema.Args {
			fmt.Fprintln(w, pipelineFlagLine(a))
		}
		if schema.Extra {
			fmt.Fprintln(w, "  (additional unrecognized flags are forwarded to the pipeline)")
		}
		fmt.Fprintln(w)
	}
	if len(schema.Examples) > 0 {
		fmt.Fprintln(w, "EXAMPLES")
		for i, ex := range schema.Examples {
			if i > 0 {
				fmt.Fprintln(w)
			}
			if ex.Comment != "" {
				fmt.Fprintf(w, "  # %s\n", ex.Comment)
			}
			fmt.Fprintf(w, "  %s\n", ex.Command)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "SPARKWING FLAGS")
	for _, d := range cmdRun.Flags {
		head := "--" + d.Name
		if d.Short != "" {
			head = "-" + d.Short + ", " + head
		}
		if d.Argument != "" {
			head += " " + d.Argument
		}
		fmt.Fprintf(w, "  %-30s %s\n", head, d.Desc)
	}
	fmt.Fprintln(w, "\nSee `sparkwing run --help` for prose explanations of each sparkwing flag.")
}

func pipelineFlagLine(a nodeproto.ArgumentDescription) string {
	tag := "[optional]"
	if a.Required {
		tag = "[required]"
	}
	head := "--" + a.Name
	if a.Short != "" {
		head = "-" + a.Short + ", " + head
	}
	head += " <" + a.Type + ">"
	var suffix strings.Builder
	if a.Default != nil {
		fmt.Fprintf(&suffix, " (default: %s)", *a.Default)
	}
	if len(a.Enum) > 0 {
		fmt.Fprintf(&suffix, " [enum: %s]", strings.Join(a.Enum, "|"))
	}
	if a.Secret {
		suffix.WriteString(" [secret]")
	}
	if a.JobID != "" {
		fmt.Fprintf(&suffix, " [from job %s]", a.JobID)
	}
	return fmt.Sprintf("  %-30s %s %s  %s%s", head, tag, a.Type, a.Description, suffix.String())
}
