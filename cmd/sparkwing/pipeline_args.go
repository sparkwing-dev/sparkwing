package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/sparkwingruntime"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// safety: a cache miss describes the binary again so the reason it cannot
// describe reaches the operator instead of an empty pipeline list. A run that
// executes in another checkout binds that checkout's pipeline names, so it is
// described there and never from the source's cache.
func (r *pipelineRun) describedPipeline(pipelineName string) (sparkwing.DescribePipeline, error) {
	var schemas []sparkwing.DescribePipeline
	bindDir := r.opts.ExecutionDir
	if bindDir == "" {
		bindDir = r.sparkwingDir
		if key, err := bincache.PipelineCacheKey(r.sparkwingDir); err == nil {
			schemas = readDescribeFile(describeCachePath(key))
		}
	}
	if schemas == nil {
		bin := r.uncachedBinary
		if bin == "" {
			bin = r.lease.Path()
		}
		doc, err := describeBinary(r.ctx, bindDir, bin)
		if err != nil {
			return sparkwing.DescribePipeline{}, err
		}
		schemas = doc.Pipelines
	}
	names := make([]string, 0, len(schemas))
	for _, s := range schemas {
		if s.Name == pipelineName {
			return s, nil
		}
		names = append(names, s.Name)
	}
	msg := fmt.Sprintf("unknown pipeline %q", pipelineName)
	if suggestion := sparkwingruntime.SuggestClosest(pipelineName, names); suggestion != "" {
		msg += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return sparkwing.DescribePipeline{}, fmt.Errorf("%s", msg)
}

func parsePipelineArgs(schema sparkwing.DescribePipeline, args []string) (map[string]string, error) {
	byName := map[string]sparkwing.DescribeArg{}
	byShort := map[string]sparkwing.DescribeArg{}
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
		if found {
			if err := arg.CheckEnum(value); err != nil {
				return nil, err
			}
		}
		out[name] = value
	}
	return out, nil
}

func printPipelineHelp(w io.Writer, schema sparkwing.DescribePipeline) {
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
	for _, d := range sparkwing.SparkwingFlagDocs() {
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

func pipelineFlagLine(a sparkwing.DescribeArg) string {
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
	if a.Default != "" {
		fmt.Fprintf(&suffix, " (default: %s)", a.Default)
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
	return fmt.Sprintf("  %-30s %s %s  %s%s", head, tag, a.Type, a.Desc, suffix.String())
}
