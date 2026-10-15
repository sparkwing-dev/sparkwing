//go:build unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
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
	describe := `"ok":true,"result":{"pipelines":[{"name":"Deploy","args":[{"name":"region","type":"string","required":true},{"name":"image","type":"string","required":true,"job_id":"rollout"}]}]}}`
	bin, _ := fakePlanSDK(t, describe, fixturePlan, fixtureEval)
	result, err := planOverProtocol(t.Context(), pipelineentry.Binary(bin), filepath.Dir(dir), "deploy-prod", []string{"--region", "us-east", "--image", "release"}, "plan")
	if err != nil || result.pipeline != "deploy-prod" || result.args["region"] != "us-east" || result.args["image"] != "release" {
		t.Fatalf("inspection lost the alias schema: %+v %v", result, err)
	}
}

func TestPipelineAliasInspectionSelectsEntrypointInV2Request(t *testing.T) {
	dir, _ := aliasProject(t)
	describe := `"ok":true,"result":{"pipelines":[{"name":"Deploy","args":[{"name":"region","type":"string","required":true},{"name":"image","type":"string","required":true,"job_id":"rollout"}]}]}}`
	bin, requests := fakePlanSDK(t, describe, fixturePlan, fixtureEval)
	if _, err := planOverProtocol(t.Context(), pipelineentry.Binary(bin), filepath.Dir(dir), "deploy-prod", []string{"--region", "us-east", "--image", "release"}, "plan"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var req nodeproto.Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatal(err)
		}
		if req.Op == nodeproto.OpPlan {
			if req.Pipeline != "Deploy" || req.Run == nil || req.Run.Pipeline != "deploy-prod" || req.Args["region"] != "us-east" || req.Args["image"] != "release" {
				t.Fatalf("selected identity lost: %+v", req)
			}
			return
		}
	}
	t.Fatal("no plan request")
}

func TestPipelineAliasInspectionUsesSDKAndAuthorContext(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a Go SDK pipeline")
	}
	dir, _ := aliasProject(t)
	writeSDKFixture(t, dir, `package main
import (
    "context"
    sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type Inputs struct { Region string `+"`flag:\"region\" required:\"true\"`"+` }
type RolloutArgs struct { Image string `+"`flag:\"image\" required:\"true\"`"+` }
type Rollout struct { sw.Base; sw.WithArgs[RolloutArgs] }
func (*Rollout) Work(w *sw.Work) (*sw.WorkStep, error) {
    return sw.Step(w, "ship", func(context.Context) error { return nil }), nil
}
type Deploy struct { sw.Base }
func (Deploy) Plan(_ context.Context, plan *sw.Plan, in Inputs, rc sw.RunContext) error {
    id := "discovery"
    if rc.Pipeline != "" { id = rc.Pipeline + "-" + in.Region }
    sw.Job(plan, id, &Rollout{})
    return nil
}
func main() {
    sw.Register[Inputs]("Deploy", func() sw.Pipeline[Inputs] { return Deploy{} })
    sw.Main()
}
`)
	binary := filepath.Join(t.TempDir(), "pipeline")
	command := exec.CommandContext(t.Context(), "go", "-C", dir, "build", "-mod=mod", "-o", binary, ".")
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build SDK fixture: %v: %s", err, output)
	}
	for _, alias := range []string{"deploy-prod", "deploy-stage"} {
		planned, err := planOverProtocol(t.Context(), pipelineentry.Binary(binary), filepath.Dir(dir), alias, []string{"--region", "us-east", "--image", "release"}, "plan")
		if err != nil {
			t.Fatal(err)
		}
		if planned.pipeline != alias || len(planned.doc.Jobs) != 1 || planned.doc.Jobs[0].ID != alias+"-us-east" {
			t.Fatalf("author context lost the alias or inputs: %+v", planned)
		}
	}
	for _, args := range [][]string{{"--image", "release"}, {"--region", "us-east"}} {
		if _, err := planOverProtocol(t.Context(), pipelineentry.Binary(binary), filepath.Dir(dir), "deploy-prod", args, "plan"); !isInvalidArgs(err) {
			t.Fatalf("required pipeline/job argument accepted: %v: %v", args, err)
		}
	}
}
