//go:build unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func TestPipelineAliasSourceUsesTheInvokingCheckoutBinding(t *testing.T) {
	caller, _ := aliasProject(t)
	source := filepath.Join(t.TempDir(), ".sparkwing")
	writeRepoFile(t, filepath.Join(source, "go.mod"), "module sourcefixture\n\ngo 1.26\n")
	writeRepoFile(t, filepath.Join(source, "sparkwing.yaml"), "pipelines:\n  - name: deploy-prod\n    entrypoint: Other\n")
	fixture := filepath.Join(t.TempDir(), "pipeline")
	protocolFixture(t, fixture, filepath.Join(t.TempDir(), "starts"), `[{"name":"Deploy","args":[{"name":"image","type":"string","required":true,"job_id":"rollout"}]}]`)
	entry, err := inspectionCommand(pipelineentry.Binary(fixture), filepath.Dir(caller), "deploy-prod")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := entry.describe(t.Context())
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

func TestPipelineInspectionSelectsRegisteredNameInV2Request(t *testing.T) {
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

func TestPipelineAliasDiscoveryUsesConfiguredEntrypoint(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	projectAt(t, "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n  - name: deploy-stage\n    entrypoint: Deploy\n")
	dir, err := findSparkwingDir()
	if err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{ sw.Base }
func (pipeline) ShortHelp() string { return "entrypoint metadata" }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p, "verify", func(context.Context) error { return nil })
 return nil
}
type unrelated struct{ pipeline }
func (unrelated) ShortHelp() string { return "unrelated metadata" }
func main() {
 sw.Register("Deploy", func() sw.Pipeline[sw.NoInputs] { return pipeline{} })
 sw.Register("deploy-prod", func() sw.Pipeline[sw.NoInputs] { return unrelated{} })
 sw.Main()
}
`)
	binary := filepath.Join(t.TempDir(), "pipeline")
	command := exec.CommandContext(t.Context(), "go", "-C", dir, "build", "-mod=mod", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build SDK fixture: %v: %s", err, output)
	}
	key, err := bincache.PipelineCacheKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDescribeCache(t.Context(), dir, key, pipelineentry.Binary(binary)); err != nil {
		t.Fatal(err)
	}
	rows, err := gatherPipelinesCatalog(false)
	if err != nil || len(rows) != 2 {
		t.Fatalf("catalog=%+v err=%v", rows, err)
	}
	for index, name := range []string{"deploy-prod", "deploy-stage"} {
		if rows[index].Name != name || rows[index].Entrypoint != "Deploy" || rows[index].Short != "entrypoint metadata" {
			t.Fatalf("catalog=%+v", rows)
		}
	}
	var output strings.Builder
	if err := runInternalCompletePipelines(&output, nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(output.String(), "\n"); got != "deploy-prod\t\tpipeline\tentrypoint metadata\ndeploy-stage\t\tpipeline\tentrypoint metadata" {
		t.Fatalf("completion=%q", got)
	}
}
