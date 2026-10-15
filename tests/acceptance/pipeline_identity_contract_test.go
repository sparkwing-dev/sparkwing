package acceptance_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

func TestSurfacePipelineIdentityContract(t *testing.T) {
	cfg, err := projectconfig.Parse([]byte("pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n  - name: deploy-stage\n    entrypoint: Deploy\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"deploy-prod", "deploy-stage"} {
		for _, prefix := range [][]string{{"run", alias}, {"pipeline", "trigger", alias, "--profile", "prod"}} {
			inv, err := clisurface.Parse("sparkwing", append(prefix, "--", "--region", "us-east", "--image", "release"))
			if err != nil {
				t.Fatal(err)
			}
			identity, err := clisurface.ResolvePipeline(cfg, inv.Pipeline)
			if err != nil || identity.Name != alias || identity.Entrypoint != "Deploy" || len(inv.Args) != 4 {
				t.Fatalf("invoked identity: %+v %v", identity, err)
			}
		}
	}
	identity, err := clisurface.ResolvePipeline(cfg, "deploy-prod")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Pipelines[0].Entrypoint = "Changed"
	if identity.Entrypoint != "Deploy" || identity.Name != "deploy-prod" {
		t.Fatalf("captured identity changed with the config: %+v", identity)
	}
	for _, config := range []*projectconfig.Config{nil, cfg} {
		identity, err := clisurface.ResolvePipeline(config, "Standalone")
		if err != nil || identity.Name != "Standalone" || identity.Entrypoint != "Standalone" {
			t.Fatalf("unconfigured entrypoint: %+v %v", identity, err)
		}
	}
	for _, config := range []*projectconfig.Config{
		{Pipelines: []projectconfig.Pipeline{{Name: "deploy-prod"}}},
		{Pipelines: []projectconfig.Pipeline{{Name: "deploy-prod", Entrypoint: "Deploy"}, {Name: "deploy-prod", Entrypoint: "Other"}}},
	} {
		if _, err := clisurface.ResolvePipeline(config, "deploy-prod"); err == nil {
			t.Fatal("invalid project supplied an execution identity")
		}
	}
	if _, err := clisurface.ResolvePipeline(nil, ""); err == nil {
		t.Fatal("empty invocation supplied an execution identity")
	}
}

func TestSurfacePlanArgumentsContract(t *testing.T) {
	inv, err := clisurface.Parse("sparkwing", []string{"pipeline", "plan", "--pipeline", "deploy-prod", "--", "--region", "us-east", "--image", "release"})
	if err != nil || inv.Value("pipeline") != "deploy-prod" || len(inv.Args) != 4 {
		t.Fatalf("plan arguments: %+v %v", inv, err)
	}
	if _, err := clisurface.Parse("sparkwing", []string{"pipeline", "plan", "deploy-prod"}); err == nil {
		t.Fatal("plan accepted a pipeline positional")
	}
}
