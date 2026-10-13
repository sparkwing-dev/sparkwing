package main

import (
	"bytes"
	"maps"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var argsFixture = sparkwing.DescribePipeline{
	Name: "deploy",
	Help: "Ships the service.",
	Args: []sparkwing.DescribeArg{
		{Name: "env", Short: "e", Type: "string", Required: true, Desc: "target environment", Enum: []string{"dev", "prod"}},
		{Name: "replicas", Type: "int", Default: "2", Desc: "pod count", JobID: "rollout"},
		{Name: "force", Type: "bool", Desc: "skip checks"},
		{Name: "token", Type: "string", Secret: true},
	},
	Examples: []sparkwing.Example{{Comment: "ship to dev", Command: "sparkwing run deploy --env dev"}},
}

func TestParsePipelineArgsFromDescribe(t *testing.T) {
	extra := argsFixture
	extra.Extra = true
	for _, tc := range []struct {
		name   string
		schema sparkwing.DescribePipeline
		args   []string
		want   map[string]string
		err    string
	}{
		{name: "long and equals", schema: argsFixture, args: []string{"--env", "dev", "--replicas=3"}, want: map[string]string{"env": "dev", "replicas": "3"}},
		{name: "short maps to the long name", schema: argsFixture, args: []string{"-e", "prod"}, want: map[string]string{"env": "prod"}},
		{name: "bool takes no value", schema: argsFixture, args: []string{"--force", "--env=dev"}, want: map[string]string{"force": "true", "env": "dev"}},
		{name: "bool with an explicit value", schema: argsFixture, args: []string{"--force=false"}, want: map[string]string{"force": "false"}},
		{name: "enum refused", schema: argsFixture, args: []string{"--env", "qa"}, err: "env"},
		{name: "unknown flag", schema: argsFixture, args: []string{"--region", "red"}, err: "unknown flag --region"},
		{name: "unknown short flag", schema: argsFixture, args: []string{"-x"}, err: "unknown flag -x"},
		{name: "positional", schema: argsFixture, args: []string{"dev"}, err: "unexpected positional argument"},
		{name: "missing value", schema: argsFixture, args: []string{"--env"}, err: "flag --env expects a value"},
		{name: "extra bag forwards unknown flags", schema: extra, args: []string{"--region", "red", "--size=xl", "--env", "dev"}, want: map[string]string{"region": "red", "size": "xl", "env": "dev"}},
		{name: "extra bag still needs a value", schema: extra, args: []string{"--region"}, err: "flag --region expects a value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePipelineArgs(tc.schema, tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want it to mention %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tc.want) {
				t.Fatalf("args = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPipelineHelpFromDescribe(t *testing.T) {
	var buf bytes.Buffer
	printPipelineHelp(&buf, argsFixture)
	out := buf.String()
	for _, want := range []string{
		"DESCRIPTION\n  Ships the service.",
		"sparkwing run deploy --env <string> [flags]",
		"-e, --env <string>",
		"[required] string  target environment [enum: dev|prod]",
		"[optional] int  pod count (default: 2) [from job rollout]",
		"--token <string>",
		"[secret]",
		"  # ship to dev\n  sparkwing run deploy --env dev",
		"SPARKWING FLAGS",
		"--sw-dry-run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "forwarded to the pipeline") {
		t.Error("help mentions an extra bag the pipeline does not declare")
	}

	buf.Reset()
	printPipelineHelp(&buf, sparkwing.DescribePipeline{Name: "lint", Extra: true})
	if out := buf.String(); !strings.Contains(out, "No pipeline-specific flags.") || strings.Contains(out, "[flags]") {
		t.Errorf("help for a pipeline without flags:\n%s", out)
	}
}
