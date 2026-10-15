//go:build unix

package main

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func TestPipelineAliasSourceUsesTheInvokingCheckoutBinding(t *testing.T) {
	caller, _ := aliasProject(t)
	source := filepath.Join(t.TempDir(), ".sparkwing")
	writeRepoFile(t, filepath.Join(source, "go.mod"), "module sourcefixture\n\ngo 1.26\n")
	writeRepoFile(t, filepath.Join(source, "sparkwing.yaml"), "pipelines:\n  - name: deploy-prod\n    entrypoint: Other\n")
	fixture := filepath.Join(t.TempDir(), "pipeline")
	protocolFixture(t, fixture, filepath.Join(t.TempDir(), "starts"), `[{"name":"Deploy","args":[{"name":"image","type":"string","required":true,"job_id":"rollout"}]}]`)
	run := &pipelineRun{ctx: t.Context(), sparkwingDir: source, entry: pipelineentry.Binary(fixture), opts: compileOptions{ExecutionDir: caller}}
	schema, err := run.describedPipeline("deploy-prod")
	if err != nil || schema.Name != "deploy-prod" || len(schema.Args) != 1 || schema.Args[0].JobID != "rollout" {
		t.Fatalf("source changed the invocation binding: %+v %v", schema, err)
	}
}

func TestPipelineAliasInspectionParsesEntrypointArgs(t *testing.T) {
	dir, _ := aliasProject(t)
	describe := `"ok":true,"result":{"protocol":"1","pipelines":[{"name":"Deploy","args":[{"name":"region","type":"string","required":true},{"name":"image","type":"string","required":true,"job_id":"rollout"}]}]}}`
	bin, _ := fakePlanSDK(t, describe, fixturePlan, fixtureEval)
	result, err := planOverProtocol(t.Context(), pipelineentry.Binary(bin), filepath.Dir(dir), "deploy-prod", []string{"--region", "us-east", "--image", "release"}, "plan")
	if err != nil || result.pipeline != "deploy-prod" || result.args["region"] != "us-east" || result.args["image"] != "release" {
		t.Fatalf("inspection lost the alias schema: %+v %v", result, err)
	}
}
