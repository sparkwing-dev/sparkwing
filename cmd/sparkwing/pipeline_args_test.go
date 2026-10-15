package main

import (
	"bytes"
	"context"
	"maps"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func TestPipelineAliasResolvesBeforeArgumentParsing(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), ".sparkwing")
	writeRepoFile(t, filepath.Join(dir, "go.mod"), "module fixture\n\ngo 1.26\n")
	writeRepoFile(t, filepath.Join(dir, "sparkwing.yaml"), "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n")
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "pipeline-alias")
	if out, err := exec.CommandContext(ctx, "go", "-C", repo, "build", "-o", bin, "./internal/nodehost/testdata/pipelinealias").CombinedOutput(); err != nil {
		t.Fatalf("compile alias fixture: %v\n%s", err, out)
	}
	run := pipelineRun{ctx: ctx, sparkwingDir: dir, entry: pipelineentry.Binary(bin)}
	schema, err := run.describedPipeline("deploy-prod")
	if err != nil {
		t.Fatal(err)
	}
	args, err := parsePipelineArgs(schema, []string{"--target", "production", "--image", "release"})
	if err != nil || args["target"] != "production" || args["image"] != "release" || run.pipeline.Invoked != "deploy-prod" || run.pipeline.Entrypoint != "Deploy" {
		t.Fatalf("selection=%+v args=%v error=%v", run.pipeline, args, err)
	}
	var help bytes.Buffer
	printPipelineHelp(&help, schema)
	if !strings.Contains(help.String(), "sparkwing run deploy-prod --target <string> --image <string>") {
		t.Fatal("alias help lost its required pipeline or job flags:", help.String())
	}
}

var argsFixture = nodeproto.PipelineDescription{
	Name: "deploy",
	Help: "Ships the service.",
	Args: []nodeproto.ArgumentDescription{
		{Name: "env", Short: "e", Type: "string", Required: true, Description: "target environment", Enum: []string{"dev", "prod"}},
		{Name: "replicas", Type: "int", Default: new("2"), Description: "pod count", JobID: "rollout"},
		{Name: "force", Type: "bool", Description: "skip checks"},
		{Name: "token", Type: "string", Secret: true},
	},
	Examples: []nodeproto.UsageExample{{Comment: "ship to dev", Command: "sparkwing run deploy --env dev"}},
}

func TestParsePipelineArgsFromDescribe(t *testing.T) {
	extra := argsFixture
	extra.Extra = true
	for _, tc := range []struct {
		name   string
		schema nodeproto.PipelineDescription
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
	printPipelineHelp(&buf, nodeproto.PipelineDescription{Name: "lint", Extra: true})
	if out := buf.String(); !strings.Contains(out, "No pipeline-specific flags.") || strings.Contains(out, "[flags]") {
		t.Errorf("help for a pipeline without flags:\n%s", out)
	}
}

func TestPipelineArgumentEnumDoesNotEchoSecretValue(t *testing.T) {
	schema := nodeproto.PipelineDescription{Name: "demo", Args: []nodeproto.ArgumentDescription{{Name: "password", Type: "string", Secret: true, Enum: []string{"valid-private-value"}}}}
	_, err := parsePipelineArgs(schema, []string{"--password", "invalid-private-value"})
	if err == nil || strings.Contains(err.Error(), "invalid-private-value") || strings.Contains(err.Error(), "valid-private-value") {
		t.Fatalf("secret enum diagnostic: %v", err)
	}
}

func TestPipelineArgumentSliceEnumChecksEachValue(t *testing.T) {
	schema := nodeproto.PipelineDescription{Name: "demo", Args: []nodeproto.ArgumentDescription{{Name: "target", Type: "[]string", Enum: []string{"linux", "darwin"}}}}
	for _, value := range []string{"linux,darwin", "linux, darwin,", ""} {
		if _, err := parsePipelineArgs(schema, []string{"--target", value}); err != nil {
			t.Fatalf("valid list: %v", err)
		}
	}
	if _, err := parsePipelineArgs(schema, []string{"--target", "linux,unknown"}); err == nil {
		t.Fatal("invalid list member accepted")
	}
}
